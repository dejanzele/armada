package scheduler

import (
	"fmt"
	"net"
	"net/http"
	_ "net/http/pprof"
	"strings"
	"time"

	"github.com/apache/pulsar-client-go/pulsar"
	"github.com/google/uuid"
	grpc_prometheus "github.com/grpc-ecosystem/go-grpc-middleware/providers/prometheus"
	grpc_logging "github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/logging"
	"github.com/pkg/errors"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc/codes"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/armadaproject/armada/internal/common"
	"github.com/armadaproject/armada/internal/common/app"
	"github.com/armadaproject/armada/internal/common/armadacontext"
	"github.com/armadaproject/armada/internal/common/auth"
	dbcommon "github.com/armadaproject/armada/internal/common/database"
	grpcCommon "github.com/armadaproject/armada/internal/common/grpc"
	"github.com/armadaproject/armada/internal/common/health"
	log "github.com/armadaproject/armada/internal/common/logging"
	"github.com/armadaproject/armada/internal/common/profiling"
	"github.com/armadaproject/armada/internal/common/pulsarutils"
	"github.com/armadaproject/armada/internal/common/pulsarutils/jobsetevents"
	"github.com/armadaproject/armada/internal/common/pulsarutils/utils"
	"github.com/armadaproject/armada/internal/common/slices"
	"github.com/armadaproject/armada/internal/common/stringinterner"
	"github.com/armadaproject/armada/internal/common/types"
	schedulerconfig "github.com/armadaproject/armada/internal/scheduler/configuration"
	"github.com/armadaproject/armada/internal/scheduler/database"
	"github.com/armadaproject/armada/internal/scheduler/floatingresources"
	"github.com/armadaproject/armada/internal/scheduler/internaltypes"
	"github.com/armadaproject/armada/internal/scheduler/jobdb"
	"github.com/armadaproject/armada/internal/scheduler/leader"
	"github.com/armadaproject/armada/internal/scheduler/metrics"
	"github.com/armadaproject/armada/internal/scheduler/pricing"
	"github.com/armadaproject/armada/internal/scheduler/priorityoverride"
	"github.com/armadaproject/armada/internal/scheduler/queue"
	"github.com/armadaproject/armada/internal/scheduler/reports"
	"github.com/armadaproject/armada/internal/scheduler/scheduling"
	"github.com/armadaproject/armada/pkg/api"
	"github.com/armadaproject/armada/pkg/api/schedulerobjects"
	"github.com/armadaproject/armada/pkg/armadaevents"
	"github.com/armadaproject/armada/pkg/client"
	"github.com/armadaproject/armada/pkg/executorapi"
	"github.com/armadaproject/armada/pkg/metricevents"
)

// Run sets up a Scheduler application and runs it until a SIGTERM is received
func Run(config schedulerconfig.Configuration) error {
	g, ctx := armadacontext.ErrGroup(app.CreateContextWithShutdown())

	// ////////////////////////////////////////////////////////////////////////
	// Expose profiling endpoints if enabled.
	// ////////////////////////////////////////////////////////////////////////
	err := profiling.SetupPprof(config.Profiling, armadacontext.Background(), nil)
	if err != nil {
		log.Fatalf("Pprof setup failed, exiting, %v", err)
	}

	// ////////////////////////////////////////////////////////////////////////
	// Health Checks
	// ////////////////////////////////////////////////////////////////////////
	mux := http.NewServeMux()

	startupCompleteCheck := health.NewStartupCompleteChecker()
	healthChecks := health.NewMultiChecker(startupCompleteCheck)
	health.SetupHttpMux(mux, healthChecks)
	shutdownHttpServer := common.ServeHttp(uint16(config.Http.Port), mux)
	defer shutdownHttpServer()

	// ////////////////////////////////////////////////////////////////////////
	// Resource list factory
	// ////////////////////////////////////////////////////////////////////////
	resourceListFactory, err := internaltypes.NewResourceListFactory(
		config.Scheduling.SupportedResourceTypes,
		config.Scheduling.FloatingResources,
	)
	if err != nil {
		return errors.WithMessage(err, "Error with the .scheduling.supportedResourceTypes field in config")
	}
	ctx.Infof("Supported resource types: %s", resourceListFactory.SummaryString())

	floatingResourceTypes, err := floatingresources.NewFloatingResourceTypes(config.Scheduling.FloatingResources, resourceListFactory)
	if err != nil {
		return err
	}
	ctx.Infof("Floating resource types: %s", floatingResourceTypes.SummaryString())

	// List of services to run concurrently.
	// Because we want to start services only once all input validation has been completed,
	// we add all services to a slice and start them together at the end of this function.
	var services []func() error

	// ////////////////////////////////////////////////////////////////////////
	// Postgres
	// ////////////////////////////////////////////////////////////////////////
	ctx.Infof("Setting up database connections")
	db, err := dbcommon.OpenPgxPool(config.Postgres)
	if err != nil {
		return errors.WithMessage(err, "Error opening connection to postgres")
	}
	defer db.Close()
	jobRepository := database.NewPostgresJobRepository(db, int32(config.DatabaseFetchSize))
	executorRepository := database.NewPostgresExecutorRepository(db)

	// ////////////////////////////////////////////////////////////////////////
	// Queue Cache
	// ////////////////////////////////////////////////////////////////////////
	conn, err := client.CreateApiConnection(&config.ArmadaApi)
	if err != nil {
		return errors.WithMessage(err, "error creating armada api client")
	}
	defer func() {
		err := conn.Close()
		if err != nil {
			ctx.Logger().
				WithStacktrace(err).
				Warnf("Armada api client didn't close down cleanly")
		}
	}()
	armadaClient := api.NewSubmitClient(conn)
	queueCache := queue.NewQueueCache(armadaClient, config.QueueRefreshPeriod)
	queueCacheInitTimeout, cancel := armadacontext.WithTimeout(ctx, time.Second*30)
	defer cancel()
	err = queueCache.Initialise(queueCacheInitTimeout)
	if err != nil {
		ctx.Errorf("error initialising queue cache - %v", err)
	}
	services = append(services, func() error { return queueCache.Run(ctx) })

	// ////////////////////////////////////////////////////////////////////////
	// Priority override
	// ////////////////////////////////////////////////////////////////////////
	priorityOverrideProvider := priorityoverride.NewNoOpProvider()
	if config.PriorityOverride.Enabled {
		ctx.Infof("Priority Override Service configured, will fetch overrides from %s", config.PriorityOverride.ServiceUrl)
		priorityOverrideClient, err := priorityoverride.NewServiceClient(config.PriorityOverride)
		if err != nil {
			return errors.WithMessage(err, "Error creating priority override client")
		}
		provider := priorityoverride.NewServiceProvider(priorityOverrideClient, config.PriorityOverride.UpdateFrequency)
		services = append(services, func() error { return provider.Run(ctx) })
		priorityOverrideProvider = provider
	}

	// ////////////////////////////////////////////////////////////////////////
	// Pricing API
	// ////////////////////////////////////////////////////////////////////////
	marketDrivenPoolConfigs := slices.Filter(config.Scheduling.Pools, func(e schedulerconfig.PoolConfig) bool {
		return e.ExperimentalMarketScheduling != nil && e.ExperimentalMarketScheduling.Enabled
	})
	marketDrivenPools := slices.Map(marketDrivenPoolConfigs, func(e schedulerconfig.PoolConfig) string {
		return e.Name
	})

	var bidPriceProvider pricing.BidPriceProvider = pricing.NoopBidPriceProvider{}
	if config.PricingApi.Enabled {
		if config.PricingApi.DevModeEnabled {
			ctx.Infof("Pricing API Service configured with dev mode on, will get queue pricing information overrides from local stub")
			bidPriceProvider = pricing.NewLocalBidPriceService(marketDrivenPools, queueCache)
		} else {
			ctx.Infof("Pricing API Service configured, will get queue pricing information overrides from %s", config.PricingApi.ServiceUrl)
			bidRetrieverClient, err := pricing.NewBidRetrieverServiceClient(config.PricingApi)
			if err != nil {
				return errors.WithMessage(err, "Error creating bid retriever client")
			}
			bidPriceCache := pricing.NewBidPriceCache(bidRetrieverClient, config.PricingApi.UpdateFrequency)
			bidPriceProviderInitTimeout, cancel := armadacontext.WithTimeout(ctx, time.Second*30)
			defer cancel()
			err = bidPriceCache.Initialise(bidPriceProviderInitTimeout)
			if err != nil {
				ctx.Errorf("error initialising queue cache - %v", err)
			}
			services = append(services, func() error { return bidPriceCache.Run(ctx) })
			bidPriceProvider = bidPriceCache
		}
	}

	// ////////////////////////////////////////////////////////////////////////
	// Pulsar
	// ////////////////////////////////////////////////////////////////////////
	ctx.Infof("Setting up Pulsar connectivity")
	pulsarClient, err := pulsarutils.NewPulsarClient(&config.Pulsar)
	if err != nil {
		return errors.WithMessage(err, "Error creating pulsar client")
	}
	defer pulsarClient.Close()

	jobsetEventPublisher, err := NewPulsarPublisher(pulsarClient, pulsar.ProducerOptions{
		Name:             fmt.Sprintf("armada-scheduler-%s", uuid.NewString()),
		CompressionType:  config.Pulsar.CompressionType,
		CompressionLevel: config.Pulsar.CompressionLevel,
		BatchingMaxSize:  config.Pulsar.MaxAllowedMessageSize,
		Topic:            config.Pulsar.JobsetEventsTopic,
	}, config.Pulsar.MaxAllowedEventsPerMessage, config.Pulsar.MaxAllowedMessageSize, config.Pulsar.SendTimeout)
	if err != nil {
		return errors.WithMessage(err, "error creating jobset event pulsar publisher")
	}

	// Publishing metrics to pulsar is experimental.  We default to a no-op publisher and only enable a pulsar publisher
	// if the feature flag is set in config
	var metricPublisher pulsarutils.Publisher[*metricevents.Event] = pulsarutils.NoOpPublisher[*metricevents.Event]{}
	if config.PublishMetricsToPulsar {
		metricPublisher, err = pulsarutils.NewPulsarPublisher[*metricevents.Event](
			pulsarClient,
			pulsar.ProducerOptions{
				Name:             fmt.Sprintf("armada-scheduler-metrics-%s", uuid.NewString()),
				CompressionType:  config.Pulsar.CompressionType,
				CompressionLevel: config.Pulsar.CompressionLevel,
				BatchingMaxSize:  config.Pulsar.MaxAllowedMessageSize,
				Topic:            config.Pulsar.MetricEventsTopic,
			},
			utils.NoOpPreProcessor,
			// Metrics are sent to an unpartitioned pulsar topic so there is no key needed
			func(event *metricevents.Event) string {
				return ""
			},
			config.Pulsar.SendTimeout,
		)
		if err != nil {
			return errors.WithMessage(err, "error creating metric event pulsar publisher")
		}
	}
	// ////////////////////////////////////////////////////////////////////////
	// Leader Election
	// ////////////////////////////////////////////////////////////////////////
	leaderController, err := createLeaderController(ctx, config.Leader)
	if err != nil {
		return errors.WithMessage(err, "error creating leader controller")
	}
	services = append(services, func() error { return leaderController.Run(ctx) })

	// ////////////////////////////////////////////////////////////////////////
	// Executor Api
	// ////////////////////////////////////////////////////////////////////////
	ctx.Infof("Setting up executor api")
	preProcessor := jobsetevents.NewPreProcessor(config.Pulsar.MaxAllowedEventsPerMessage, config.Pulsar.MaxAllowedMessageSize)
	apiPublisher, err := pulsarutils.NewPulsarPublisher[*armadaevents.EventSequence](
		pulsarClient,
		pulsar.ProducerOptions{
			Name:             fmt.Sprintf("armada-executor-api-%s", uuid.NewString()),
			CompressionType:  config.Pulsar.CompressionType,
			CompressionLevel: config.Pulsar.CompressionLevel,
			BatchingMaxSize:  config.Pulsar.MaxAllowedMessageSize,
			Topic:            config.Pulsar.JobsetEventsTopic,
		},
		preProcessor,
		jobsetevents.RetrieveKey,
		config.Pulsar.SendTimeout,
	)
	if err != nil {
		return errors.Wrapf(err, "error creating pulsar publisher for executor api")
	}
	defer apiPublisher.Close()

	authServices, err := auth.ConfigureAuth(config.Auth)
	if err != nil {
		return errors.WithMessage(err, "error creating auth services")
	}
	grpcServer := grpcCommon.CreateGrpcServer(config.Grpc.KeepaliveParams, config.Grpc.KeepaliveEnforcementPolicy, authServices, config.Grpc.Tls, createGrpcLoggingOption())
	defer grpcServer.GracefulStop()
	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", config.Grpc.Port))
	if err != nil {
		return errors.WithMessage(err, "error setting up gRPC server")
	}

	authorizer := auth.NewAuthorizer(
		auth.NewPrincipalPermissionChecker(
			config.Auth.PermissionGroupMapping,
			config.Auth.PermissionScopeMapping,
			config.Auth.PermissionClaimMapping,
		),
	)

	executorServer, err := NewExecutorApi(
		apiPublisher,
		jobRepository,
		executorRepository,
		types.AllowedPriorities(config.Scheduling.PriorityClasses),
		slices.Map(config.Scheduling.SupportedResourceTypes, func(rt schedulerconfig.ResourceType) string { return rt.Name }),
		config.Scheduling.NodeIdLabel,
		config.Scheduling.PriorityClassNameOverride,
		config.Scheduling.PriorityClasses,
		authorizer,
	)
	if err != nil {
		return errors.WithMessage(err, "error creating executorApi")
	}
	executorapi.RegisterExecutorApiServer(grpcServer, executorServer)
	services = append(services, func() error {
		ctx.Infof("Executor api listening on %s", lis.Addr())
		return grpcServer.Serve(lis)
	})
	services = append(services, grpcCommon.CreateShutdownHandler(ctx, 5*time.Second, grpcServer))

	// ////////////////////////////////////////////////////////////////////////
	// Scheduler Reports
	// ////////////////////////////////////////////////////////////////////////
	schedulingContextRepository := reports.NewSchedulingContextRepository()
	reportServer := reports.NewServer(schedulingContextRepository)

	clientMetrics := grpc_prometheus.NewClientMetrics(
		grpc_prometheus.WithClientHandlingTimeHistogram(),
	)
	prometheus.MustRegister(clientMetrics)

	leaderClientConnectionProvider := leader.NewLeaderConnectionProvider(leaderController, config.Leader, clientMetrics)
	schedulingSchedulerReportingServer := reports.NewLeaderProxyingSchedulingReportsServer(reportServer, leaderClientConnectionProvider)
	schedulerobjects.RegisterSchedulerReportingServer(grpcServer, schedulingSchedulerReportingServer)

	// ////////////////////////////////////////////////////////////////////////
	// Scheduling
	// ////////////////////////////////////////////////////////////////////////
	ctx.Infof("setting up scheduling loop")

	submitChecker := NewSubmitChecker(
		config.Scheduling,
		executorRepository,
		queueCache,
		floatingResourceTypes,
		resourceListFactory,
	)
	submitCheckerInitTimeout, cancel := armadacontext.WithTimeout(ctx, time.Second*30)
	defer cancel()
	err = submitChecker.Initialise(submitCheckerInitTimeout)
	if err != nil {
		ctx.Errorf("error initialising submit checker - %v", err)
	}
	services = append(services, func() error {
		return submitChecker.Run(ctx)
	})

	shortJobPenalty := scheduling.NewShortJobPenalty(config.Scheduling.GetShortJobPenaltyCutoffs())
	stringInterner := stringinterner.New(config.InternedStringsCacheSize)
	schedulingAlgo, err := scheduling.NewFairSchedulingAlgo(
		config.Scheduling,
		config.MaxSchedulingDuration,
		executorRepository,
		queueCache,
		schedulingContextRepository,
		resourceListFactory,
		floatingResourceTypes,
		priorityOverrideProvider,
		shortJobPenalty,
	)
	if err != nil {
		return errors.WithMessage(err, "error creating scheduling algo")
	}
	jobDb := jobdb.NewJobDb(
		config.Scheduling.PriorityClasses,
		config.Scheduling.DefaultPriorityClassName,
		stringInterner,
		resourceListFactory,
	)

	schedulerMetrics, err := metrics.New(
		config.Metrics.TrackedErrorRegexes,
		config.Metrics.TrackedResourceNames,
		config.Metrics.JobCheckpointIntervals,
		config.Metrics.JobStateMetricsResetInterval,
		metricPublisher,
	)
	if err != nil {
		return err
	}
	if err := prometheus.Register(schedulerMetrics); err != nil {
		return errors.WithStack(err)
	}

	scheduler, err := NewScheduler(
		jobDb,
		jobRepository,
		executorRepository,
		schedulingAlgo,
		leaderController,
		jobsetEventPublisher,
		submitChecker,
		config.CyclePeriod,
		config.SchedulePeriod,
		config.ExecutorTimeout,
		shortJobPenalty,
		config.Scheduling.MaxRetries+1,
		config.Scheduling.NodeIdLabel,
		schedulerMetrics,
		bidPriceProvider,
		marketDrivenPools,
	)
	if err != nil {
		return errors.WithMessage(err, "error creating scheduler")
	}
	if config.Scheduling.EnableAssertions {
		scheduler.EnableAssertions()
	}
	services = append(services, func() error { return scheduler.Run(ctx) })

	// ////////////////////////////////////////////////////////////////////////
	// Metrics
	// ////////////////////////////////////////////////////////////////////////
	metricsCollector := NewMetricsCollector(
		scheduler.jobDb,
		queueCache,
		bidPriceProvider,
		executorRepository,
		config.Scheduling.Pools,
		config.Metrics.RefreshInterval,
		floatingResourceTypes,
	)
	if err := prometheus.Register(metricsCollector); err != nil {
		return errors.WithStack(err)
	}
	services = append(services, func() error { return metricsCollector.Run(ctx) })
	shutdownMetricServer := common.ServeMetrics(config.Metrics.Port)
	defer shutdownMetricServer()

	// start all services
	for _, service := range services {
		g.Go(service)
	}

	// Mark startup as complete, will allow the health check to return healthy
	startupCompleteCheck.MarkComplete()

	return g.Wait()
}

func createLeaderController(ctx *armadacontext.Context, config schedulerconfig.LeaderConfig) (leader.LeaderController, error) {
	switch mode := strings.ToLower(config.Mode); mode {
	case "standalone":
		ctx.Infof("Scheduler will run in standalone mode")
		return leader.NewStandaloneLeaderController(), nil
	case "kubernetes":
		ctx.Infof("Scheduler will run kubernetes mode")
		clusterConfig, err := loadClusterConfig(ctx)
		if err != nil {
			return nil, errors.Wrapf(err, "Error creating kubernetes client")
		}
		clientSet, err := kubernetes.NewForConfig(clusterConfig)
		if err != nil {
			return nil, errors.Wrapf(err, "Error creating kubernetes client")
		}
		leaderController := leader.NewKubernetesLeaderController(config, clientSet.CoordinationV1())
		leaderStatusMetrics := leader.NewLeaderStatusMetricsCollector(config.PodName)
		leaderController.RegisterListener(leaderStatusMetrics)
		prometheus.MustRegister(leaderStatusMetrics)
		return leaderController, nil
	default:
		return nil, errors.Errorf("%s is not a value leader mode", config.Mode)
	}
}

func loadClusterConfig(ctx *armadacontext.Context) (*rest.Config, error) {
	config, err := rest.InClusterConfig()
	if errors.Is(err, rest.ErrNotInCluster) {
		ctx.Info("Running with default client configuration")
		rules := clientcmd.NewDefaultClientConfigLoadingRules()
		overrides := &clientcmd.ConfigOverrides{}
		return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
	}
	ctx.Info("Running with in cluster client configuration")
	return config, err
}

// This changes the default grpc logging to log OK messages at trace level
// The reason for doing this are:
//   - Reduced logging
//   - We only care about failures, so lets only log failures
//   - We normally use these logs to work out who is calling us, however the Executor API is not public
//     and is only called by other Armada components
func createGrpcLoggingOption() grpc_logging.Option {
	return grpc_logging.WithLevels(func(code codes.Code) grpc_logging.Level {
		switch code {
		case codes.OK:
			return grpc_logging.LevelDebug
		default:
			return grpc_logging.DefaultServerCodeToLevel(code)
		}
	})
}
