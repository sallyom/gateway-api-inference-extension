# Distributed Tracing for Gateway API Inference Extension

This document describes OpenTelemetry distributed tracing support in the Gateway API Inference Extension (EPP - External Processing Protocol).

## Overview

The gateway implements OpenTelemetry tracing with custom spans to provide observability into request routing, admission control, and scheduling decisions. The instrumentation captures latency metrics, routing decisions, and admission control outcomes while following metadata-only tracing principles.

### Main Spans

- **`gateway.request`**: Full request lifecycle from arrival to completion (SERVER span)
  - Captures: Model names, token counts, streaming mode, pod info
  - Created per-request in the Process method

- **`gateway.director.handle_request`**: Admission control and routing decision (INTERNAL span)
  - Captures: Candidate pods, admission priority, routing result, target pod
  - Created in Director.HandleRequest method

- **`gateway.scheduler.schedule`**: Scheduling algorithm execution (INTERNAL span)
  - Captures: Candidate pods, scheduling profiles, selected pod
  - Created in Scheduler.Schedule method

## Configuration

### Prerequisites

The gateway uses OpenTelemetry Go SDK. Ensure the following dependencies are available:
```go
go.opentelemetry.io/otel
go.opentelemetry.io/otel/attribute
go.opentelemetry.io/otel/codes
go.opentelemetry.io/otel/trace
go.opentelemetry.io/otel/propagation
```

### Initialization

Tracing must be initialized in your application startup code. The gateway uses the global OpenTelemetry tracer provider:

```go
import (
    "go.opentelemetry.io/otel"
    "go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
    "go.opentelemetry.io/otel/propagation"
    sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func initTracing(ctx context.Context) (*sdktrace.TracerProvider, error) {
    // Create OTLP exporter
    exporter, err := otlptracegrpc.New(ctx,
        otlptracegrpc.WithEndpoint("otel-collector:4317"),
        otlptracegrpc.WithInsecure(),
    )
    if err != nil {
        return nil, err
    }

    // Create tracer provider
    tp := sdktrace.NewTracerProvider(
        sdktrace.WithBatcher(exporter),
        sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(0.1))),
    )

    // Set global tracer provider
    otel.SetTracerProvider(tp)

    // Set W3C trace context propagator
    otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
        propagation.TraceContext{},
        propagation.Baggage{},
    ))

    return tp, nil
}
```

## Spans and Attributes

### gateway.request (SERVER)

Created in `handlers/server.go:Process()` for each incoming request.

**Attributes:**
- `gen_ai.request.model` (string): Incoming model name from request
- `gateway.target_model` (string): Target model after rewrite (if different)
- `gateway.request.size_bytes` (int): Size of request body in bytes
- `gateway.response.streaming` (bool): Whether response is streamed
- `gen_ai.usage.prompt_tokens` (int): Number of prompt tokens
- `gen_ai.usage.completion_tokens` (int): Number of completion tokens

**Example:**
```go
span.SetAttributes(
    attribute.String("gen_ai.request.model", "llama-2-7b"),
    attribute.String("gateway.target_model", "llama-2-7b-chat"),
    attribute.Int("gateway.request.size_bytes", 1024),
    attribute.Bool("gateway.response.streaming", true),
    attribute.Int("gen_ai.usage.prompt_tokens", 128),
    attribute.Int("gen_ai.usage.completion_tokens", 512),
)
```

### gateway.director.handle_request (INTERNAL)

Created in `requestcontrol/director.go:HandleRequest()` during admission control.

**Attributes:**
- `gateway.admission.candidate_pods` (int): Number of candidate pods for routing
- `gateway.admission.priority` (int): Request priority from InferenceObjective
- `gateway.admission.result` (string): Admission outcome ("admitted" | "rejected")
- `gateway.target_pod.name` (string): Selected pod name (if admitted)

**Example:**
```go
span.SetAttributes(
    attribute.Int("gateway.admission.candidate_pods", 3),
    attribute.Int("gateway.admission.priority", 100),
    attribute.String("gateway.admission.result", "admitted"),
    attribute.String("gateway.target_pod.name", "vllm-pod-0"),
)
```

### gateway.scheduler.schedule (INTERNAL)

Created in `scheduling/scheduler.go:Schedule()` during pod selection.

**Attributes:**
- `gateway.scheduler.candidate_pods` (int): Number of candidate pods
- `gateway.request.id` (string): Request identifier
- `gateway.scheduler.result` (string): Scheduling outcome ("scheduled" | "failed")
- `gateway.target_pod.name` (string): Selected pod name (if scheduled)
- `gateway.target_pod.namespace` (string): Selected pod namespace

**Example:**
```go
span.SetAttributes(
    attribute.Int("gateway.scheduler.candidate_pods", 3),
    attribute.String("gateway.request.id", "req-12345"),
    attribute.String("gateway.scheduler.result", "scheduled"),
    attribute.String("gateway.target_pod.name", "vllm-pod-1"),
    attribute.String("gateway.target_pod.namespace", "default"),
)
```

## Trace Context Propagation

The gateway automatically propagates W3C trace context to backend services (e.g., vLLM):

### Outbound Propagation

When proxying requests to backend pods, the gateway injects trace headers in `handlers/request.go:generateHeaders()`:

```go
// Inject trace context headers for distributed tracing
traceHeaders := make(map[string]string)
propagator := otel.GetTextMapPropagator()
propagator.Inject(ctx, propagation.MapCarrier(traceHeaders))

// Add trace headers to backend request
for key, value := range traceHeaders {
    headers = append(headers, &configPb.HeaderValueOption{
        Header: &configPb.HeaderValue{
            Key:      key,
            RawValue: []byte(value),
        },
    })
}
```

This propagates:
- `traceparent`: W3C trace context (trace ID, span ID, trace flags)
- `tracestate`: Vendor-specific trace state

### Inbound Propagation

The gateway automatically extracts trace context from incoming Envoy requests through the standard W3C propagation mechanism.

## Example Distributed Trace

A complete distributed trace for an inference request:

```
gateway.request (2150ms) [SERVER]
├── gateway.director.handle_request (45ms) [INTERNAL]
│   ├── Attributes:
│   │   ├── gateway.admission.candidate_pods: 3
│   │   ├── gateway.admission.priority: 100
│   │   ├── gateway.admission.result: "admitted"
│   │   └── gateway.target_pod.name: "vllm-pod-1"
│   │
│   └── gateway.scheduler.schedule (38ms) [INTERNAL]
│       ├── Attributes:
│       │   ├── gateway.scheduler.candidate_pods: 3
│       │   ├── gateway.request.id: "req-12345"
│       │   ├── gateway.scheduler.result: "scheduled"
│       │   ├── gateway.target_pod.name: "vllm-pod-1"
│       │   └── gateway.target_pod.namespace: "default"
│
└── [HTTP call to vLLM via trace propagation]
    └── llm_request (2100ms) [SERVER] ← trace continues in vLLM
        ├── Attributes:
        │   ├── gen_ai.latency.time_to_first_token: 0.045
        │   ├── gen_ai.latency.e2e: 2.100
        │   ├── gen_ai.usage.prompt_tokens: 128
        │   └── gen_ai.usage.completion_tokens: 512
```

## Viewing Traces

### With Jaeger

1. Deploy OpenTelemetry Collector and Jaeger:
```bash
# docker-compose.yml
version: '3'
services:
  jaeger:
    image: jaegertracing/all-in-one:latest
    ports:
      - "16686:16686"  # Jaeger UI
      - "14250:14250"  # gRPC collector

  otel-collector:
    image: otel/opentelemetry-collector:latest
    command: ["--config=/etc/otel-collector-config.yaml"]
    volumes:
      - ./otel-collector-config.yaml:/etc/otel-collector-config.yaml
    ports:
      - "4317:4317"  # OTLP gRPC
```

2. Initialize tracing in gateway pointing to collector at `otel-collector:4317`

3. View traces at http://localhost:16686

### Query Examples

**Find all gateway requests:**
```
service="gateway-api-inference-extension" AND operation="gateway.request"
```

**Find admitted requests:**
```
gateway.admission.result="admitted"
```

**Find requests routed to specific pod:**
```
gateway.target_pod.name="vllm-pod-1"
```

**Find scheduling failures:**
```
gateway.scheduler.result="failed"
```

## Security Considerations

The instrumentation follows metadata-only tracing:

**Captured:**
- ✅ Model names (without sensitive model data)
- ✅ Token counts (prompt and completion)
- ✅ Pod names and namespaces
- ✅ Admission control decisions
- ✅ Scheduling outcomes
- ✅ Request sizes (bytes)

**NOT Captured:**
- ❌ Actual prompts or prompt text
- ❌ Generated completions or output text
- ❌ Token IDs or vocabulary
- ❌ User identifiers or auth tokens
- ❌ Request bodies or sensitive headers

## Performance Impact

- **Overhead when tracing disabled**: ~0% (no tracer initialized)
- **Overhead when tracing enabled**: <1% latency increase
  - Span creation is lightweight
  - Uses OpenTelemetry BatchSpanProcessor for efficient export
  - Minimal overhead from attribute setting
  - Sampling at 10% reduces export volume

## Integration with vLLM

When the gateway routes requests to vLLM, trace context is automatically propagated:

```
Client Request
    ↓ (includes traceparent header if present)
gateway.request [SERVER]
    ├── gateway.director.handle_request [INTERNAL]
    │   └── gateway.scheduler.schedule [INTERNAL]
    ↓ (trace context injected in headers)
HTTP Request to vLLM Pod
    ↓ (traceparent + tracestate headers)
llm_request [SERVER] ← trace continues
```

This creates an end-to-end distributed trace across the entire inference stack.

## Troubleshooting

### No traces appearing in Jaeger

1. Check OpenTelemetry collector is reachable:
   ```bash
   telnet otel-collector 4317
   ```

2. Verify tracer provider initialization:
   ```go
   tp, err := initTracing(ctx)
   if err != nil {
       log.Fatal(err)
   }
   defer tp.Shutdown(ctx)
   ```

3. Check sampling rate (may be set too low):
   ```go
   sdktrace.WithSampler(sdktrace.AlwaysSample())  // For debugging
   ```

### Trace context not propagating to vLLM

1. Verify propagator is set:
   ```go
   otel.SetTextMapPropagator(propagation.TraceContext{})
   ```

2. Check headers are being injected in `generateHeaders()`:
   ```go
   // Add logging to verify
   log.Printf("Injected trace headers: %v", traceHeaders)
   ```

3. Verify vLLM is configured to accept trace headers

### High tracing overhead

1. Reduce sampling rate:
   ```go
   sdktrace.WithSampler(sdktrace.TraceIDRatioBased(0.01))  // 1% sampling
   ```

2. Use batch span processor (default):
   ```go
   sdktrace.WithBatcher(exporter)
   ```

## References

- [OpenTelemetry Go Documentation](https://opentelemetry.io/docs/languages/go/)
- [GenAI Semantic Conventions](https://opentelemetry.io/docs/specs/semconv/gen-ai/)
- [W3C Trace Context](https://www.w3.org/TR/trace-context/)
- [Envoy External Processing](https://www.envoyproxy.io/docs/envoy/latest/api-v3/service/ext_proc/v3/external_processor.proto)
- [llm-d Distributed Tracing Proposal](../../llm-d/docs/proposals/distributed-tracing.md)
- [vLLM Tracing Documentation](../../vllm/docs/TRACING.md)
