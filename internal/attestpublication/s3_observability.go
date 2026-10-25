package attestpublication

import (
	"context"

	awsmiddleware "github.com/aws/aws-sdk-go-v2/aws/middleware"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const s3AttemptSpanName = "attest.s3.attempt"

var s3AttemptOperations = map[string]struct{}{
	"CompleteMultipartUpload": {},
	"CreateMultipartUpload":   {},
	"HeadObject":              {},
	"ListParts":               {},
	"UploadPart":              {},
}

// s3AttemptClientOption records one private OTel span for every physical SDK
// request attempt. It is attached only to the client created from an Attest
// upload grant, so ordinary CLI S3 traffic is never counted as publication.
func s3AttemptClientOption(options *s3.Options) {
	options.Interceptors.AddAfterAttempt(s3AttemptInterceptor{})
}

type s3AttemptInterceptor struct{}

func (s3AttemptInterceptor) AfterAttempt(ctx context.Context, in *smithyhttp.InterceptorContext) error {
	outcome := "failure"
	if in.Output != nil {
		outcome = "success"
	}
	_, span := otel.Tracer("github.com/sageox/ox/internal/attestpublication").Start(ctx, s3AttemptSpanName,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("attest.s3.operation", boundedS3Operation(awsmiddleware.GetOperationName(ctx))),
			attribute.String("attest.s3.outcome", outcome),
		))
	if outcome == "failure" {
		span.SetStatus(codes.Error, outcome)
	}
	span.End()
	return nil
}

func boundedS3Operation(operation string) string {
	if _, ok := s3AttemptOperations[operation]; ok {
		return operation
	}
	return "other"
}
