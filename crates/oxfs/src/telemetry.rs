//! OpenTelemetry plumbing for spans emitted by oxFS.
//!
//! oxFS owns span creation because it owns the filesystem behaviors worth
//! observing. Hosts provide the SageOx endpoint and bearer token; Honeycomb
//! credentials never cross this API.

use opentelemetry::trace::TracerProvider as _;
use opentelemetry::{KeyValue, global};
use opentelemetry_otlp::{Protocol, WithExportConfig as _, WithHttpConfig as _};
use opentelemetry_sdk::Resource;
use opentelemetry_sdk::trace::SdkTracerProvider;
use std::collections::HashMap;
use std::time::Duration;
use tracing_subscriber::layer::SubscriberExt as _;
use tracing_subscriber::util::SubscriberInitExt as _;

const TRACE_PATH: &str = "/api/v1/otlp/v1/traces";

#[derive(Clone, Eq, PartialEq)]
pub struct TelemetryConfig {
    pub api_endpoint: String,
    pub bearer_token: String,
    pub service_name: String,
}

impl TelemetryConfig {
    pub fn new(api_endpoint: impl Into<String>, bearer_token: impl Into<String>) -> Self {
        Self {
            api_endpoint: api_endpoint.into(),
            bearer_token: bearer_token.into(),
            service_name: "oxfs".into(),
        }
    }
}

/// Keeps the SDK provider alive and flushes completed oxFS spans on drop.
pub struct Telemetry {
    provider: Option<SdkTracerProvider>,
}

impl Telemetry {
    pub fn disabled() -> Self {
        Self { provider: None }
    }

    /// Installs the oxFS trace pipeline.
    ///
    /// Missing credentials disable export. Initialization failures are returned
    /// to the host, which should continue without tracing.
    pub fn init(config: TelemetryConfig) -> Result<Self, Box<dyn std::error::Error + Send + Sync>> {
        if config.api_endpoint.trim().is_empty() || config.bearer_token.trim().is_empty() {
            return Ok(Self::disabled());
        }

        let endpoint = trace_endpoint(&config.api_endpoint)?;
        let exporter = opentelemetry_otlp::SpanExporter::builder()
            .with_http()
            .with_protocol(Protocol::HttpBinary)
            .with_endpoint(endpoint)
            .with_timeout(Duration::from_secs(2))
            .with_headers(HashMap::from([(
                "authorization".into(),
                format!("Bearer {}", config.bearer_token),
            )]))
            .build()?;
        let resource = Resource::builder()
            .with_service_name(config.service_name)
            .with_attributes([
                KeyValue::new("service.version", env!("CARGO_PKG_VERSION")),
                KeyValue::new("host.os", std::env::consts::OS),
                KeyValue::new("host.arch", std::env::consts::ARCH),
            ])
            .build();
        let provider = SdkTracerProvider::builder()
            .with_resource(resource)
            .with_batch_exporter(exporter)
            .build();
        let tracer = provider.tracer("oxfs");

        tracing_subscriber::registry()
            .with(tracing_opentelemetry::layer().with_tracer(tracer))
            .try_init()?;
        global::set_tracer_provider(provider.clone());

        Ok(Self {
            provider: Some(provider),
        })
    }

    pub fn enabled(&self) -> bool {
        self.provider.is_some()
    }

    /// Creates the process root span. Future spans emitted inside oxFS inherit
    /// this context while the returned span is entered by the host.
    pub fn session_span(&self, harness: &'static str, cache_max_bytes: u64) -> tracing::Span {
        tracing::info_span!(
            "oxfs.session",
            otel.kind = "client",
            oxfs.harness = harness,
            cache.max_bytes = cache_max_bytes,
            telemetry.enabled = self.enabled(),
            otel.status_code = tracing::field::Empty,
            error.message = tracing::field::Empty,
        )
    }
}

impl Drop for Telemetry {
    fn drop(&mut self) {
        if let Some(provider) = self.provider.take()
            && let Err(error) = provider.shutdown()
        {
            eprintln!("level=WARN action=otel_shutdown error={error:?}");
        }
    }
}

fn trace_endpoint(api_endpoint: &str) -> Result<String, &'static str> {
    let endpoint = api_endpoint.trim().trim_end_matches('/');
    if !(endpoint.starts_with("https://") || endpoint.starts_with("http://")) {
        return Err("SageOx API endpoint must use http or https");
    }
    Ok(format!("{endpoint}{TRACE_PATH}"))
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::{Read, Write};
    use std::net::TcpListener;
    use std::sync::mpsc;
    use std::thread;

    #[test]
    fn trace_endpoint_targets_sageox_proxy() {
        assert_eq!(
            trace_endpoint("https://api.sageox.ai/").unwrap(),
            "https://api.sageox.ai/api/v1/otlp/v1/traces"
        );
        assert_eq!(
            trace_endpoint("http://127.0.0.1:8080").unwrap(),
            "http://127.0.0.1:8080/api/v1/otlp/v1/traces"
        );
    }

    #[test]
    fn trace_endpoint_rejects_bare_hosts() {
        assert!(trace_endpoint("api.sageox.ai").is_err());
    }

    #[test]
    fn missing_auth_disables_export() {
        let telemetry = Telemetry::init(TelemetryConfig::new("https://api.sageox.ai", ""))
            .expect("missing auth should be a no-op");
        assert!(!telemetry.enabled());
    }

    #[test]
    fn exporter_posts_to_authenticated_sageox_proxy() {
        let listener = TcpListener::bind("127.0.0.1:0").unwrap();
        let address = listener.local_addr().unwrap();
        let (sent, received) = mpsc::channel();
        let server = thread::spawn(move || {
            let (mut stream, _) = listener.accept().unwrap();
            let mut request = Vec::new();
            let mut buffer = [0; 4096];
            loop {
                let read = stream.read(&mut buffer).unwrap();
                if read == 0 {
                    break;
                }
                request.extend_from_slice(&buffer[..read]);
                if request.windows(4).any(|window| window == b"\r\n\r\n") {
                    break;
                }
            }
            sent.send(String::from_utf8_lossy(&request).into_owned())
                .unwrap();
            stream
                .write_all(b"HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n")
                .unwrap();
        });

        let telemetry = Telemetry::init(TelemetryConfig::new(
            format!("http://{address}"),
            "test-jwt",
        ))
        .unwrap();
        let span = telemetry.session_span("test", 1024);
        drop(span);
        drop(telemetry);

        let request = received.recv_timeout(Duration::from_secs(3)).unwrap();
        let lowercase = request.to_ascii_lowercase();
        assert!(request.starts_with("POST /api/v1/otlp/v1/traces HTTP/1.1\r\n"));
        assert!(lowercase.contains("authorization: bearer test-jwt\r\n"));
        assert!(lowercase.contains("content-type: application/x-protobuf\r\n"));
        server.join().unwrap();
    }
}
