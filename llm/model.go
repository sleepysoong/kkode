package llm

import "fmt"

type Model struct {
	ID           string
	Provider     string
	DisplayName  string
	Family       string
	Capabilities Capabilities
	Context      ModelContext
	Pricing      *ModelPricing
}

type ModelContext struct {
	InputTokens  int
	OutputTokens int
}

type ModelPricing struct {
	InputPerMillion           float64
	CachedInputPerMillion     float64
	OutputPerMillion          float64
	ReasoningOutputPerMillion float64
	Currency                  string
}

type ModelRegistry struct {
	models map[string]Model
}

func NewModelRegistry(models ...Model) *ModelRegistry {
	r := &ModelRegistry{models: map[string]Model{}}
	for _, m := range models {
		r.Register(m)
	}
	return r
}

func (r *ModelRegistry) Register(model Model) {
	if r.models == nil {
		r.models = map[string]Model{}
	}
	r.models[model.Provider+":"+model.ID] = model
}

func (r *ModelRegistry) Get(provider, id string) (Model, bool) {
	if r == nil || r.models == nil {
		return Model{}, false
	}
	m, ok := r.models[provider+":"+id]
	return m, ok
}

func (r *ModelRegistry) MustGet(provider, id string) Model {
	m, ok := r.Get(provider, id)
	if !ok {
		panic(fmt.Sprintf("model %s:%s not registered", provider, id))
	}
	return m
}
