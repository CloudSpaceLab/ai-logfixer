package laravel

import "github.com/CloudSpaceLab/ai-logfixer/internal/truth"

var _ truth.FrameworkTruthAdapter = TruthAdapter{}

type TruthAdapter struct{}

func (TruthAdapter) Name() string {
	return "laravel"
}

func (TruthAdapter) DetectSuppression(signal truth.ErrorSignal, files []truth.SourceFile) []truth.SuppressionSite {
	return truth.StaticSuppressionDetector{}.Detect(signal, files)
}

func (TruthAdapter) PlanReveal(signal truth.ErrorSignal, sites []truth.SuppressionSite) truth.RevealPlan {
	return truth.DefaultRevealPlanner{}.Plan(signal, sites)
}

func (TruthAdapter) BuildFixBundle(result truth.TruthRecoveryResult) (truth.FixBundle, error) {
	return truth.DefaultFixBundleBuilder{}.Build(result)
}

func TruthSignal(service string, source string, message string, environment truth.Environment) truth.ErrorSignal {
	return truth.ErrorSignal{
		Service:     service,
		Framework:   "laravel",
		Source:      source,
		Message:     message,
		Environment: environment,
	}
}
