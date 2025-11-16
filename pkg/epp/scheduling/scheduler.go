/*
Copyright 2025 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package scheduling implements request scheduling algorithms.
package scheduling

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	oteltrace "go.opentelemetry.io/otel/trace"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/metrics"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"
)

// NewSchedulerWithConfig returns a new scheduler with the given scheduler plugins configuration.
func NewSchedulerWithConfig(config *SchedulerConfig) *Scheduler {
	return &Scheduler{
		profileHandler: config.profileHandler,
		profiles:       config.profiles,
	}
}

type Scheduler struct {
	profileHandler framework.ProfileHandler
	profiles       map[string]*framework.SchedulerProfile
}

// Schedule finds the target pod based on metrics and the requested lora adapter.
func (s *Scheduler) Schedule(ctx context.Context, request *types.LLMRequest, candidatePods []types.Pod) (*types.SchedulingResult, error) {
	tracer := otel.Tracer("gateway-api-inference-extension")
	ctx, span := tracer.Start(ctx, "gateway.scheduler.schedule",
		oteltrace.WithSpanKind(oteltrace.SpanKindInternal),
	)
	defer span.End()

	loggerVerbose := log.FromContext(ctx).V(logutil.VERBOSE)

	scheduleStart := time.Now()
	defer func() {
		metrics.RecordSchedulerE2ELatency(time.Since(scheduleStart))
	}()

	// Add span attributes for scheduling context
	span.SetAttributes(
		attribute.Int("gateway.scheduler.candidate_pods", len(candidatePods)),
		attribute.String("gateway.request.id", request.RequestId),
	)

	profileRunResults := map[string]*types.ProfileRunResult{}
	cycleState := types.NewCycleState()

	for { // get the next set of profiles to run iteratively based on the request and the previous execution results
		loggerVerbose.Info("Running profile handler, Pick profiles", "plugin", s.profileHandler.TypedName())
		before := time.Now()
		profiles := s.profileHandler.Pick(ctx, cycleState, request, s.profiles, profileRunResults)
		metrics.RecordPluginProcessingLatency(framework.ProfilePickerExtensionPoint, s.profileHandler.TypedName().Type, s.profileHandler.TypedName().Name, time.Since(before))
		loggerVerbose.Info("Completed running profile handler Pick profiles successfully", "plugin", s.profileHandler.TypedName(), "result", profiles)
		if len(profiles) == 0 { // profile picker didn't pick any profile to run
			break
		}

		for name, profile := range profiles {
			loggerVerbose.Info("Running scheduler profile", "profile", name)
			// run the selected profiles and collect results (current code runs all profiles)
			profileRunResult, err := profile.Run(ctx, request, cycleState, candidatePods)
			if err != nil {
				loggerVerbose.Info("failed to run scheduler profile", "profile", name, "error", err.Error())
			} else {
				loggerVerbose.Info("Completed running scheduler profile succuessfully", "profile", name)
			}

			profileRunResults[name] = profileRunResult // if profile failed to run, the run result is nil
		}
	}

	if len(profileRunResults) == 0 {
		return nil, fmt.Errorf("failed to run any scheduler profile for request %s", request.RequestId)
	}

	loggerVerbose.Info("Running profile handler, ProcessResults", "plugin", s.profileHandler.TypedName())
	before := time.Now()
	result, err := s.profileHandler.ProcessResults(ctx, cycleState, request, profileRunResults)
	metrics.RecordPluginProcessingLatency(framework.ProcessProfilesResultsExtensionPoint, s.profileHandler.TypedName().Type, s.profileHandler.TypedName().Name, time.Since(before))
	loggerVerbose.Info("Completed running profile handler ProcessResults successfully", "plugin", s.profileHandler.TypedName())

	// Add scheduling result attributes to span
	if result != nil && result.TargetPod != nil {
		span.SetAttributes(
			attribute.String("gateway.scheduler.result", "scheduled"),
			attribute.String("gateway.target_pod.name", result.TargetPod.GetName()),
			attribute.String("gateway.target_pod.namespace", result.TargetPod.GetNamespace()),
		)
	} else if err != nil {
		span.SetAttributes(
			attribute.String("gateway.scheduler.result", "failed"),
		)
	}

	return result, err
}
