package sentryjin

import (
	"context"
	"fmt"
	"github.com/getsentry/sentry-go"
	"github.com/juanjiTech/jin"
	"net"
	"net/http"
	"os"
	"reflect"
	"strings"
	"time"
)

// The identifier of the Gin SDK.
const sdkIdentifier = "sentry.go.jin"

type handler struct {
	repanic         bool
	waitForDelivery bool
	timeout         time.Duration
}

type Options struct {
	// Repanic configures whether Sentry should repanic after recovery, in most cases it should be set to true,
	// as gin.Default includes it's own Recovery middleware what handles http responses.
	Repanic bool
	// WaitForDelivery configures whether you want to block the request before moving forward with the response.
	// Because Gin's default Recovery handler doesn't restart the application,
	// it's safe to either skip this option or set it to false.
	WaitForDelivery bool
	// Timeout for the event delivery requests.
	Timeout time.Duration
}

// New returns a function that satisfies gin.HandlerFunc interface
// It can be used with Use() methods.
func New(options Options) jin.HandlerFunc {
	timeout := options.Timeout
	if timeout == 0 {
		timeout = 2 * time.Second
	}
	return (&handler{
		repanic:         options.Repanic,
		timeout:         timeout,
		waitForDelivery: options.WaitForDelivery,
	}).handle
}

func (h *handler) handle(c *jin.Context) {
	ctx := c.Request.Context()
	hub := GetHubFromContext(c)
	if hub == nil {
		hub = sentry.CurrentHub().Clone()
		ctx = sentry.SetHubOnContext(ctx, hub)
	}

	if client := hub.Client(); client != nil {
		client.SetSDKIdentifier(sdkIdentifier)
	}

	var transactionName string
	var transactionSource sentry.TransactionSource

	if c.FullPath() != "" {
		transactionName = c.FullPath()
		transactionSource = sentry.SourceRoute
	} else {
		transactionName = c.Request.URL.Path
		transactionSource = sentry.SourceURL
	}

	options := []sentry.SpanOption{
		sentry.WithOpName("http.server"),
		sentry.ContinueFromRequest(c.Request),
		sentry.WithTransactionSource(transactionSource),
	}

	transaction := sentry.StartTransaction(ctx,
		fmt.Sprintf("%s %s", c.Request.Method, transactionName),
		options...,
	)
	defer func() {
		transaction.Status = sentry.HTTPtoSpanStatus(c.Writer.Status())
		transaction.Finish()
	}()

	c.Request = c.Request.WithContext(transaction.Context())
	hub.Scope().SetRequest(c.Request)
	defer h.recoverWithSentry(hub, c.Request)
	c.Map(hub)
	c.Next()
}

func (h *handler) recoverWithSentry(hub *sentry.Hub, r *http.Request) {
	if err := recover(); err != nil {
		if !isBrokenPipeError(err) {
			eventID := hub.RecoverWithContext(
				context.WithValue(r.Context(), sentry.RequestContextKey, r),
				err,
			)
			if eventID != nil && h.waitForDelivery {
				hub.Flush(h.timeout)
			}
		}
		if h.repanic {
			panic(err)
		}
	}
}

// Check for a broken connection, as this is what Gin does already.
func isBrokenPipeError(err interface{}) bool {
	if netErr, ok := err.(*net.OpError); ok {
		if sysErr, ok := netErr.Err.(*os.SyscallError); ok {
			if strings.Contains(strings.ToLower(sysErr.Error()), "broken pipe") ||
				strings.Contains(strings.ToLower(sysErr.Error()), "connection reset by peer") {
				return true
			}
		}
	}
	return false
}

// GetHubFromContext retrieves attached *sentry.Hub instance from gin.Context.
func GetHubFromContext(ctx *jin.Context) *sentry.Hub {
	if ctx.Value(reflect.TypeOf((*sentry.Hub)(nil))).IsValid() {
		return ctx.Value(reflect.TypeOf((*sentry.Hub)(nil))).Interface().(*sentry.Hub)
	}
	return nil
}
