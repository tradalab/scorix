package llm

// For a run of several requests, such as an agent loop: tokens and time add,
// so the total is what the run cost.
func (u Usage) Add(v Usage) Usage {
	return Usage{
		PromptTokens:     u.PromptTokens + v.PromptTokens,
		CacheReadTokens:  u.CacheReadTokens + v.CacheReadTokens,
		CacheWriteTokens: u.CacheWriteTokens + v.CacheWriteTokens,
		CompletionTokens: u.CompletionTokens + v.CompletionTokens,
		PromptDuration:   u.PromptDuration + v.PromptDuration,
		GenerateDuration: u.GenerateDuration + v.GenerateDuration,
	}
}

// Per million tokens, in whatever currency the app keeps its prices in. The
// framework holds no price list: prices change, differ by provider and by
// contract, and no API reports them.
type Price struct {
	Input  float64
	Output float64
	// Zero bills those tokens at Input: a price list that does not split them
	// out is charging them as input.
	CacheRead  float64
	CacheWrite float64
}

func (u Usage) Cost(p Price) float64 {
	read, write := p.CacheRead, p.CacheWrite
	if read == 0 {
		read = p.Input
	}
	if write == 0 {
		write = p.Input
	}
	// A provider that reports cached tokens but not the total would otherwise
	// be billed negative input.
	plain := max(u.PromptTokens-u.CacheReadTokens-u.CacheWriteTokens, 0)
	return (float64(plain)*p.Input + float64(u.CacheReadTokens)*read + float64(u.CacheWriteTokens)*write + float64(u.CompletionTokens)*p.Output) / 1e6
}
