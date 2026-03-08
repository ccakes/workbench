package spanbuf

import "time"

type serviceEdgeKey struct {
	from string
	to   string
}

type serviceEdge struct {
	CallCount    int
	ErrorCount   int
	TotalDuration time.Duration
}

type serviceMap struct {
	edges map[serviceEdgeKey]*serviceEdge
}

func newServiceMap() *serviceMap {
	return &serviceMap{
		edges: make(map[serviceEdgeKey]*serviceEdge),
	}
}

func (m *serviceMap) addEdge(key serviceEdgeKey, duration time.Duration, isError bool) {
	e, ok := m.edges[key]
	if !ok {
		e = &serviceEdge{}
		m.edges[key] = e
	}
	e.CallCount++
	e.TotalDuration += duration
	if isError {
		e.ErrorCount++
	}
}

func (m *serviceMap) removeEdge(key serviceEdgeKey) {
	e, ok := m.edges[key]
	if !ok {
		return
	}
	e.CallCount--
	if e.CallCount <= 0 {
		delete(m.edges, key)
	}
}

func (m *serviceMap) snapshot() ServiceMapSnapshot {
	edges := make([]ServiceEdge, 0, len(m.edges))
	for k, v := range m.edges {
		if v.CallCount > 0 {
			avgDuration := time.Duration(0)
			if v.CallCount > 0 {
				avgDuration = v.TotalDuration / time.Duration(v.CallCount)
			}
			edges = append(edges, ServiceEdge{
				From:        k.from,
				To:          k.to,
				CallCount:   v.CallCount,
				ErrorCount:  v.ErrorCount,
				AvgDuration: avgDuration,
			})
		}
	}
	return ServiceMapSnapshot{Edges: edges}
}

// ServiceEdge represents a directed edge between two services.
type ServiceEdge struct {
	From        string
	To          string
	CallCount   int
	ErrorCount  int
	AvgDuration time.Duration
}

// ServiceMapSnapshot is a point-in-time view of service interactions.
type ServiceMapSnapshot struct {
	Edges []ServiceEdge
}

// Services returns the unique service names in the map.
func (s ServiceMapSnapshot) Services() []string {
	seen := make(map[string]bool)
	for _, e := range s.Edges {
		seen[e.From] = true
		seen[e.To] = true
	}
	result := make([]string, 0, len(seen))
	for svc := range seen {
		result = append(result, svc)
	}
	return result
}
