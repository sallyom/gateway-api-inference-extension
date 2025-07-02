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

// Package tracing provides OpenTelemetry tracing infrastructure for the gateway-api-inference-extension
package tracing

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.4.0"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	ServiceName = "gateway-api-inference-extension"

	envOTELTracingEnabled   = "OTEL_TRACING_ENABLED"
	envOTELExporterEndpoint = "OTEL_EXPORTER_OTLP_ENDPOINT"
	envOTELServiceName      = "OTEL_SERVICE_NAME"
	envOTELSamplingRate     = "OTEL_SAMPLING_RATE"
)

type Config struct {
	Enabled          bool
	ExporterEndpoint string
	SamplingRate     float64
	ServiceName      string
}

func NewConfigFromEnv() *Config {
	config := &Config{
		Enabled:          false,
		ExporterEndpoint: "http://localhost:4317",
		SamplingRate:     0.1,
		ServiceName:      ServiceName,
	}

	if enabled := os.Getenv(envOTELTracingEnabled); enabled != "" {
		if enabledBool, err := strconv.ParseBool(enabled); err == nil {
			config.Enabled = enabledBool
		}
	}

	if endpoint := os.Getenv(envOTELExporterEndpoint); endpoint != "" {
		config.ExporterEndpoint = endpoint
	}

	if serviceName := os.Getenv(envOTELServiceName); serviceName != "" {
		config.ServiceName = serviceName
	}

	if samplingRate := os.Getenv(envOTELSamplingRate); samplingRate != "" {
		if rate, err := strconv.ParseFloat(samplingRate, 64); err == nil {
			config.SamplingRate = rate
		}
	}

	return config
}

// Initialize sets up OpenTelemetry tracing with the given configuration.
// It always sets up context propagation, even if tracing is disabled.
func Initialize(ctx context.Context, config *Config) (func(), error) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	if !config.Enabled {
		// Return a no-op shutdown function if tracing is disabled
		return func() {}, nil
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	conn, err := grpc.NewClient(config.ExporterEndpoint,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create gRPC connection to collector: %w", err)
	}

	exporter, err := otlptracegrpc.New(ctx, otlptracegrpc.WithGRPCConn(conn))
	if err != nil {
		return nil, fmt.Errorf("failed to create OTLP trace exporter: %w", err)
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceNameKey.String(config.ServiceName),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create resource: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.TraceIDRatioBased(config.SamplingRate)),
	)

	otel.SetTracerProvider(tp)

	return func() {
		_ = tp.Shutdown(context.Background())
	}, nil
}

// StartSpan starts a new span with the gateway service tracer
func StartSpan(ctx context.Context, name string) (context.Context, trace.Span) {
	tracer := otel.Tracer(ServiceName)
	return tracer.Start(ctx, name)
}
