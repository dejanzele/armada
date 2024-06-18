// Code generated by go-swagger; DO NOT EDIT.

package models

// This file was generated by the swagger tool.
// Editing this file might prove futile when you re-run the swagger generate command

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/go-openapi/errors"
	"github.com/go-openapi/strfmt"
	"github.com/go-openapi/swag"
	"github.com/go-openapi/validate"
)

// Job job
//
// swagger:model job
type Job struct {

	// annotations
	// Required: true
	Annotations map[string]string `json:"annotations"`

	// cancel reason
	CancelReason *string `json:"cancelReason,omitempty"`

	// cancelled
	// Format: date-time
	Cancelled *strfmt.DateTime `json:"cancelled,omitempty"`

	// cluster
	// Required: true
	Cluster string `json:"cluster"`

	// cpu
	// Required: true
	CPU int64 `json:"cpu"`

	// duplicate
	// Required: true
	Duplicate bool `json:"duplicate"`

	// ephemeral storage
	// Required: true
	EphemeralStorage int64 `json:"ephemeralStorage"`

	// exit code
	ExitCode *int32 `json:"exitCode,omitempty"`

	// gpu
	// Required: true
	Gpu int64 `json:"gpu"`

	// job Id
	// Required: true
	// Min Length: 1
	JobID string `json:"jobId"`

	// job set
	// Required: true
	// Min Length: 1
	JobSet string `json:"jobSet"`

	// last active run Id
	LastActiveRunID *string `json:"lastActiveRunId,omitempty"`

	// last transition time
	// Required: true
	// Min Length: 1
	// Format: date-time
	LastTransitionTime strfmt.DateTime `json:"lastTransitionTime"`

	// memory
	// Required: true
	Memory int64 `json:"memory"`

	// namespace
	Namespace *string `json:"namespace,omitempty"`

	// node
	Node *string `json:"node,omitempty"`

	// owner
	// Required: true
	// Min Length: 1
	Owner string `json:"owner"`

	// priority
	// Required: true
	Priority int64 `json:"priority"`

	// priority class
	PriorityClass *string `json:"priorityClass,omitempty"`

	// queue
	// Required: true
	// Min Length: 1
	Queue string `json:"queue"`

	// runs
	// Required: true
	Runs []*Run `json:"runs"`

	// runtime seconds
	// Required: true
	RuntimeSeconds int32 `json:"runtimeSeconds"`

	// state
	// Required: true
	// Enum: [QUEUED PENDING RUNNING SUCCEEDED FAILED CANCELLED PREEMPTED LEASED]
	State string `json:"state"`

	// submitted
	// Required: true
	// Min Length: 1
	// Format: date-time
	Submitted strfmt.DateTime `json:"submitted"`
}

// Validate validates this job
func (m *Job) Validate(formats strfmt.Registry) error {
	var res []error

	if err := m.validateAnnotations(formats); err != nil {
		res = append(res, err)
	}

	if err := m.validateCancelled(formats); err != nil {
		res = append(res, err)
	}

	if err := m.validateCluster(formats); err != nil {
		res = append(res, err)
	}

	if err := m.validateCPU(formats); err != nil {
		res = append(res, err)
	}

	if err := m.validateDuplicate(formats); err != nil {
		res = append(res, err)
	}

	if err := m.validateEphemeralStorage(formats); err != nil {
		res = append(res, err)
	}

	if err := m.validateGpu(formats); err != nil {
		res = append(res, err)
	}

	if err := m.validateJobID(formats); err != nil {
		res = append(res, err)
	}

	if err := m.validateJobSet(formats); err != nil {
		res = append(res, err)
	}

	if err := m.validateLastTransitionTime(formats); err != nil {
		res = append(res, err)
	}

	if err := m.validateMemory(formats); err != nil {
		res = append(res, err)
	}

	if err := m.validateOwner(formats); err != nil {
		res = append(res, err)
	}

	if err := m.validatePriority(formats); err != nil {
		res = append(res, err)
	}

	if err := m.validateQueue(formats); err != nil {
		res = append(res, err)
	}

	if err := m.validateRuns(formats); err != nil {
		res = append(res, err)
	}

	if err := m.validateRuntimeSeconds(formats); err != nil {
		res = append(res, err)
	}

	if err := m.validateState(formats); err != nil {
		res = append(res, err)
	}

	if err := m.validateSubmitted(formats); err != nil {
		res = append(res, err)
	}

	if len(res) > 0 {
		return errors.CompositeValidationError(res...)
	}
	return nil
}

func (m *Job) validateAnnotations(formats strfmt.Registry) error {

	if err := validate.Required("annotations", "body", m.Annotations); err != nil {
		return err
	}

	return nil
}

func (m *Job) validateCancelled(formats strfmt.Registry) error {
	if swag.IsZero(m.Cancelled) { // not required
		return nil
	}

	if err := validate.FormatOf("cancelled", "body", "date-time", m.Cancelled.String(), formats); err != nil {
		return err
	}

	return nil
}

func (m *Job) validateCluster(formats strfmt.Registry) error {

	if err := validate.RequiredString("cluster", "body", m.Cluster); err != nil {
		return err
	}

	return nil
}

func (m *Job) validateCPU(formats strfmt.Registry) error {

	if err := validate.Required("cpu", "body", int64(m.CPU)); err != nil {
		return err
	}

	return nil
}

func (m *Job) validateDuplicate(formats strfmt.Registry) error {

	if err := validate.Required("duplicate", "body", bool(m.Duplicate)); err != nil {
		return err
	}

	return nil
}

func (m *Job) validateEphemeralStorage(formats strfmt.Registry) error {

	if err := validate.Required("ephemeralStorage", "body", int64(m.EphemeralStorage)); err != nil {
		return err
	}

	return nil
}

func (m *Job) validateGpu(formats strfmt.Registry) error {

	if err := validate.Required("gpu", "body", int64(m.Gpu)); err != nil {
		return err
	}

	return nil
}

func (m *Job) validateJobID(formats strfmt.Registry) error {

	if err := validate.RequiredString("jobId", "body", m.JobID); err != nil {
		return err
	}

	if err := validate.MinLength("jobId", "body", m.JobID, 1); err != nil {
		return err
	}

	return nil
}

func (m *Job) validateJobSet(formats strfmt.Registry) error {

	if err := validate.RequiredString("jobSet", "body", m.JobSet); err != nil {
		return err
	}

	if err := validate.MinLength("jobSet", "body", m.JobSet, 1); err != nil {
		return err
	}

	return nil
}

func (m *Job) validateLastTransitionTime(formats strfmt.Registry) error {

	if err := validate.Required("lastTransitionTime", "body", strfmt.DateTime(m.LastTransitionTime)); err != nil {
		return err
	}

	if err := validate.MinLength("lastTransitionTime", "body", m.LastTransitionTime.String(), 1); err != nil {
		return err
	}

	if err := validate.FormatOf("lastTransitionTime", "body", "date-time", m.LastTransitionTime.String(), formats); err != nil {
		return err
	}

	return nil
}

func (m *Job) validateMemory(formats strfmt.Registry) error {

	if err := validate.Required("memory", "body", int64(m.Memory)); err != nil {
		return err
	}

	return nil
}

func (m *Job) validateOwner(formats strfmt.Registry) error {

	if err := validate.RequiredString("owner", "body", m.Owner); err != nil {
		return err
	}

	if err := validate.MinLength("owner", "body", m.Owner, 1); err != nil {
		return err
	}

	return nil
}

func (m *Job) validatePriority(formats strfmt.Registry) error {

	if err := validate.Required("priority", "body", int64(m.Priority)); err != nil {
		return err
	}

	return nil
}

func (m *Job) validateQueue(formats strfmt.Registry) error {

	if err := validate.RequiredString("queue", "body", m.Queue); err != nil {
		return err
	}

	if err := validate.MinLength("queue", "body", m.Queue, 1); err != nil {
		return err
	}

	return nil
}

func (m *Job) validateRuns(formats strfmt.Registry) error {

	if err := validate.Required("runs", "body", m.Runs); err != nil {
		return err
	}

	for i := 0; i < len(m.Runs); i++ {
		if swag.IsZero(m.Runs[i]) { // not required
			continue
		}

		if m.Runs[i] != nil {
			if err := m.Runs[i].Validate(formats); err != nil {
				if ve, ok := err.(*errors.Validation); ok {
					return ve.ValidateName("runs" + "." + strconv.Itoa(i))
				} else if ce, ok := err.(*errors.CompositeError); ok {
					return ce.ValidateName("runs" + "." + strconv.Itoa(i))
				}
				return err
			}
		}

	}

	return nil
}

func (m *Job) validateRuntimeSeconds(formats strfmt.Registry) error {

	if err := validate.Required("runtimeSeconds", "body", int32(m.RuntimeSeconds)); err != nil {
		return err
	}

	return nil
}

var jobTypeStatePropEnum []interface{}

func init() {
	var res []string
	if err := json.Unmarshal([]byte(`["QUEUED","PENDING","RUNNING","SUCCEEDED","FAILED","CANCELLED","PREEMPTED","LEASED"]`), &res); err != nil {
		panic(err)
	}
	for _, v := range res {
		jobTypeStatePropEnum = append(jobTypeStatePropEnum, v)
	}
}

const (

	// JobStateQUEUED captures enum value "QUEUED"
	JobStateQUEUED string = "QUEUED"

	// JobStatePENDING captures enum value "PENDING"
	JobStatePENDING string = "PENDING"

	// JobStateRUNNING captures enum value "RUNNING"
	JobStateRUNNING string = "RUNNING"

	// JobStateSUCCEEDED captures enum value "SUCCEEDED"
	JobStateSUCCEEDED string = "SUCCEEDED"

	// JobStateFAILED captures enum value "FAILED"
	JobStateFAILED string = "FAILED"

	// JobStateCANCELLED captures enum value "CANCELLED"
	JobStateCANCELLED string = "CANCELLED"

	// JobStatePREEMPTED captures enum value "PREEMPTED"
	JobStatePREEMPTED string = "PREEMPTED"

	// JobStateLEASED captures enum value "LEASED"
	JobStateLEASED string = "LEASED"
)

// prop value enum
func (m *Job) validateStateEnum(path, location string, value string) error {
	if err := validate.EnumCase(path, location, value, jobTypeStatePropEnum, true); err != nil {
		return err
	}
	return nil
}

func (m *Job) validateState(formats strfmt.Registry) error {

	if err := validate.RequiredString("state", "body", m.State); err != nil {
		return err
	}

	// value enum
	if err := m.validateStateEnum("state", "body", m.State); err != nil {
		return err
	}

	return nil
}

func (m *Job) validateSubmitted(formats strfmt.Registry) error {

	if err := validate.Required("submitted", "body", strfmt.DateTime(m.Submitted)); err != nil {
		return err
	}

	if err := validate.MinLength("submitted", "body", m.Submitted.String(), 1); err != nil {
		return err
	}

	if err := validate.FormatOf("submitted", "body", "date-time", m.Submitted.String(), formats); err != nil {
		return err
	}

	return nil
}

// ContextValidate validate this job based on the context it is used
func (m *Job) ContextValidate(ctx context.Context, formats strfmt.Registry) error {
	var res []error

	if err := m.contextValidateRuns(ctx, formats); err != nil {
		res = append(res, err)
	}

	if len(res) > 0 {
		return errors.CompositeValidationError(res...)
	}
	return nil
}

func (m *Job) contextValidateRuns(ctx context.Context, formats strfmt.Registry) error {

	for i := 0; i < len(m.Runs); i++ {

		if m.Runs[i] != nil {
			if err := m.Runs[i].ContextValidate(ctx, formats); err != nil {
				if ve, ok := err.(*errors.Validation); ok {
					return ve.ValidateName("runs" + "." + strconv.Itoa(i))
				} else if ce, ok := err.(*errors.CompositeError); ok {
					return ce.ValidateName("runs" + "." + strconv.Itoa(i))
				}
				return err
			}
		}

	}

	return nil
}

// MarshalBinary interface implementation
func (m *Job) MarshalBinary() ([]byte, error) {
	if m == nil {
		return nil, nil
	}
	return swag.WriteJSON(m)
}

// UnmarshalBinary interface implementation
func (m *Job) UnmarshalBinary(b []byte) error {
	var res Job
	if err := swag.ReadJSON(b, &res); err != nil {
		return err
	}
	*m = res
	return nil
}
