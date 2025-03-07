package main

import (
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"

	"github.com/armadaproject/armada/internal/common"
	"github.com/armadaproject/armada/internal/common/armadacontext"
	gateway "github.com/armadaproject/armada/internal/common/grpc"
	"github.com/armadaproject/armada/internal/common/health"
	"github.com/armadaproject/armada/internal/common/logging"
	log "github.com/armadaproject/armada/internal/common/logging"
	"github.com/armadaproject/armada/internal/common/profiling"
	"github.com/armadaproject/armada/internal/server"
	"github.com/armadaproject/armada/internal/server/configuration"
	"github.com/armadaproject/armada/pkg/api"
	"github.com/armadaproject/armada/pkg/api/schedulerobjects"
)

const CustomConfigLocation string = "config"

func init() {
	pflag.StringSlice(
		CustomConfigLocation,
		[]string{},
		"Fully qualified path to application configuration file (for multiple config files repeat this arg or separate paths with commas)",
	)
	pflag.Parse()
}

func main() {
	logging.MustConfigureApplicationLogging()
	common.BindCommandlineArguments()

	// TODO Load relevant config in one place: don't use viper here and in LoadConfig.
	var config configuration.ArmadaConfig
	userSpecifiedConfigs := viper.GetStringSlice(CustomConfigLocation)
	common.LoadConfig(&config, "./config/server", userSpecifiedConfigs)

	log.Info("Starting...")

	// Run services within an errgroup to propagate errors between services.
	g, ctx := armadacontext.ErrGroup(armadacontext.Background())

	// Cancel the errgroup context on SIGINT and SIGTERM,
	// which shuts everything down gracefully.
	stopSignal := make(chan os.Signal, 1)
	signal.Notify(stopSignal, syscall.SIGINT, syscall.SIGTERM)
	g.Go(func() error {
		select {
		case <-ctx.Done():
			return nil
		case sig := <-stopSignal:
			// Returning an error cancels the errgroup.
			return fmt.Errorf("received signal %v", sig)
		}
	})

	// Expose profiling endpoints if enabled.
	err := profiling.SetupPprof(config.Profiling, ctx, g)
	if err != nil {
		log.Fatalf("Pprof setup failed, exiting, %v", err)
	}

	// TODO This starts a separate HTTP server. Is that intended? Should we have a single mux for everything?
	// TODO: Run in errgroup
	shutdownMetricServer := common.ServeMetrics(config.MetricsPort)
	defer shutdownMetricServer()

	// Register /health API endpoint
	mux := http.NewServeMux()
	startupCompleteCheck := health.NewStartupCompleteChecker()
	healthChecks := health.NewMultiChecker(startupCompleteCheck)
	health.SetupHttpMux(mux, healthChecks)

	// register gRPC API handlers in mux
	// TODO: Run in errgroup
	shutdownGateway := gateway.CreateGatewayHandler(
		config.GrpcPort,
		mux,
		config.GrpcGatewayPath,
		true,
		config.Grpc.Tls.Enabled,
		config.CorsAllowedOrigins,
		api.SwaggerJsonTemplate(),
		api.RegisterSubmitHandler,
		api.RegisterEventHandler,
		api.RegisterJobsHandler,
		schedulerobjects.RegisterSchedulerReportingHandler,
	)
	defer shutdownGateway()

	// start HTTP server
	// TODO: Run in errgroup
	var shutdownHttpServer func()
	if config.Grpc.Tls.Enabled {
		shutdownHttpServer = common.ServeHttps(config.HttpPort, mux, config.Grpc.Tls.CertPath, config.Grpc.Tls.KeyPath)
	} else {
		shutdownHttpServer = common.ServeHttp(config.HttpPort, mux)
	}
	defer shutdownHttpServer()

	// Start Armada server
	g.Go(func() error {
		return server.Serve(ctx, &config, healthChecks)
	})

	// Assume the server is ready if there are no errors within 10 seconds.
	go func() {
		time.Sleep(10 * time.Second)
		startupCompleteCheck.MarkComplete()
	}()

	if err := g.Wait(); err != nil {
		logging.WithStacktrace(err).Error("Armada server shut down")
	}
}
