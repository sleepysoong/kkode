package llm

func (u Usage) EstimatedCost(pricing ModelPricing) float64 {
	return (float64(u.InputTokens)/1_000_000)*pricing.InputPerMillion +
		(float64(u.OutputTokens)/1_000_000)*pricing.OutputPerMillion +
		(float64(u.ReasoningTokens)/1_000_000)*pricing.ReasoningOutputPerMillion
}
