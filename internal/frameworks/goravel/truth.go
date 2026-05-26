package goravel

import "github.com/CloudSpaceLab/ai-logfixer/internal/truth"

var _ truth.FrameworkTruthAdapter = TruthAdapter{}

type TruthAdapter struct{}

func (TruthAdapter) Name() string {
	return "goravel"
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

func TruthResultFromAnalysis(analysis Analysis) truth.TruthRecoveryResult {
	signal := truth.ErrorSignal{
		Service:     analysis.Failure.ServiceName,
		Framework:   "goravel",
		Source:      analysis.Route.HandlerFile,
		Message:     analysis.Diagnosis.SuspectedRootCause,
		Environment: truth.EnvironmentUnknown,
	}
	owner := truth.SourceOwner{
		File:       analysis.Route.HandlerFile,
		Function:   analysis.Route.ControllerType + "." + analysis.Route.HandlerMethod,
		Language:   "go",
		Framework:  "goravel",
		Confidence: 0.86,
	}
	var sites []truth.SuppressionSite
	if !analysis.PatchSafety.Safe {
		sites = append(sites, truth.SuppressionSite{
			File:       analysis.Route.HandlerFile,
			Function:   owner.Function,
			Kind:       "goravel_handler_requires_truth_recovery",
			Evidence:   analysis.HandlerExcerpt,
			CanReveal:  true,
			Confidence: 0.72,
		})
	}
	revealPlan := truth.DefaultRevealPlanner{}.Plan(signal, sites)
	return truth.TruthRecoveryResult{
		Signal:           signal,
		SuppressionSites: sites,
		RevealPlan:       revealPlan,
		SourceOwner:      owner,
	}
}
