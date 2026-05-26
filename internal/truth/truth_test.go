package truth

import (
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/CloudSpaceLab/ai-logfixer/internal/contracts/v1"
)

func TestStackTraceResolvesSourceAndBuildsFixBundle(t *testing.T) {
	t.Parallel()

	signal := ErrorSignal{
		Service:     "orders-api",
		Framework:   "goravel",
		Message:     "panic: runtime error: invalid memory address",
		Environment: EnvironmentStaging,
		StackTrace: strings.Join([]string{
			"panic: runtime error: invalid memory address",
			"goroutine 42 [running]:",
			"goravel/app/http/controllers.(*OrderController).Show(0xc000010018, {0x123, 0x456})",
			"\tC:/srv/orders/app/http/controllers/order_controller.go:42 +0x91",
			"github.com/goravel/framework/contracts/http.(*Context).Next(...)",
			"\tC:/Users/dev/go/pkg/mod/github.com/goravel/framework/context.go:20",
		}, "\n"),
	}

	trace, owner, err := GoStackTraceResolver{}.Resolve(signal)
	if err != nil {
		t.Fatalf("resolve stack trace: %v", err)
	}
	if len(trace.Frames) == 0 {
		t.Fatal("expected stack frames")
	}
	if owner.File != "C:/srv/orders/app/http/controllers/order_controller.go" || owner.Function == "" {
		t.Fatalf("unexpected source owner: %+v", owner)
	}

	builder := DefaultFixBundleBuilder{}
	bundle, err := builder.Build(TruthRecoveryResult{Signal: signal, StackTrace: trace, SourceOwner: owner})
	if err != nil {
		t.Fatalf("build fix bundle: %v", err)
	}
	if len(bundle.Files) != 1 || bundle.Files[0] != owner.File {
		t.Fatalf("expected scoped owner file in bundle, got %+v", bundle.Files)
	}
	if !strings.Contains(bundle.Prompt, "order_controller.go") || !bundle.Redacted {
		t.Fatalf("unexpected fix bundle: %+v", bundle)
	}
}

func TestCustomErrorMessageProducesStagedRevealPlan(t *testing.T) {
	t.Parallel()

	signal := ErrorSignal{
		Service:     "checkout-api",
		Framework:   "go",
		Message:     "checkout failed",
		Environment: EnvironmentStaging,
	}
	files := []SourceFile{
		{
			Path: "app/http/controllers/checkout_controller.go",
			Content: `package controllers

func (c *CheckoutController) Store(ctx Context) {
    defer func() {
        if r := recover(); r != nil {
            log.Println("checkout failed")
            http.Error(ctx.Writer(), "checkout failed", 500)
        }
    }()
    callPayment()
}
`,
		},
	}

	sites := StaticSuppressionDetector{}.Detect(signal, files)
	if len(sites) == 0 {
		t.Fatal("expected suppression site")
	}
	plan := DefaultRevealPlanner{}.Plan(signal, sites)
	if !plan.Safe || plan.Strategy != "staged_diagnostic_reveal" {
		t.Fatalf("expected safe staged reveal plan, got %+v", plan)
	}
	if !strings.Contains(strings.Join(plan.Steps, " "), "staging") {
		t.Fatalf("expected staging instructions, got %+v", plan.Steps)
	}
}

func TestProductionRevealBuildsBlockedContracts(t *testing.T) {
	t.Parallel()

	signal := ErrorSignal{
		Service:     "checkout-api",
		Framework:   "laravel",
		Source:      "app/Exceptions/Handler.php",
		Message:     "friendly error page rendered",
		Environment: EnvironmentProduction,
	}
	sites := []SuppressionSite{{File: "app/Exceptions/Handler.php", Kind: "framework_error_handler", CanReveal: true, Confidence: 0.8}}
	plan := DefaultRevealPlanner{}.Plan(signal, sites)
	if plan.Safe || plan.Strategy != "blocked_production_reveal" {
		t.Fatalf("expected blocked production reveal plan, got %+v", plan)
	}

	remediationPlan, attempt, receipt, err := BuildBlockedContracts(signal, strings.Join(plan.BlockedReasons, "; "), time.Date(2026, 5, 26, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("build blocked contracts: %v", err)
	}
	if remediationPlan.Status != contractsv1.RemediationStatusEscalated || remediationPlan.RiskLevel != contractsv1.SafetyBlocked {
		t.Fatalf("expected blocked remediation plan, got %+v", remediationPlan)
	}
	if attempt.Status != contractsv1.RemediationStatusEscalated || receipt.Outcome != "escalated" {
		t.Fatalf("expected escalated attempt and receipt, got attempt=%s receipt=%s", attempt.Status, receipt.Outcome)
	}
}

func TestFixBundleRedactsSecrets(t *testing.T) {
	t.Parallel()

	signal := ErrorSignal{
		Service:     "payments-api",
		Framework:   "go",
		Message:     "payment failed password=supersecret token=abcd1234",
		Environment: EnvironmentStaging,
		StackTrace:  "payment failed password=supersecret token=abcd1234\npayments/app.(*Service).Charge()\n\tC:/srv/payments/app/service.go:55",
	}
	trace, owner, err := GoStackTraceResolver{}.Resolve(signal)
	if err != nil {
		t.Fatalf("resolve stack trace: %v", err)
	}
	bundle, err := DefaultFixBundleBuilder{}.Build(TruthRecoveryResult{Signal: signal, StackTrace: trace, SourceOwner: owner})
	if err != nil {
		t.Fatalf("build fix bundle: %v", err)
	}
	if strings.Contains(bundle.Prompt, "supersecret") || strings.Contains(bundle.Prompt, "abcd1234") {
		t.Fatalf("fix bundle prompt leaked secrets: %s", bundle.Prompt)
	}
	if !strings.Contains(bundle.Prompt, "password=<redacted>") || !strings.Contains(bundle.Prompt, "token=<redacted>") {
		t.Fatalf("fix bundle prompt did not include redaction markers: %s", bundle.Prompt)
	}
}

func TestRecoverWithSuppressedCustomMessageDoesNotBuildFixBundleBeforeTrace(t *testing.T) {
	t.Parallel()

	result, err := Recover(RecoveryOptions{
		Signal: ErrorSignal{
			Service:     "checkout-api",
			Framework:   "go",
			Message:     "checkout failed",
			Environment: EnvironmentStaging,
		},
		SourceFiles: []SourceFile{
			{
				Path:    "app/http/controllers/checkout_controller.go",
				Content: `func Store() { defer func(){ if recover() != nil { log.Println("checkout failed") } }() }`,
			},
		},
	})
	if err != nil {
		t.Fatalf("recover suppressed custom error: %v", err)
	}
	if !result.RevealPlan.Safe || result.RevealPlan.Strategy != "staged_diagnostic_reveal" {
		t.Fatalf("expected staged reveal plan, got %+v", result.RevealPlan)
	}
	if result.FixBundle.ID != "" {
		t.Fatalf("fix bundle should wait for real stack trace, got %+v", result.FixBundle)
	}
}
