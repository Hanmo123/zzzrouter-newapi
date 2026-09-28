package service

import (
	"github.com/QuantumNous/new-api/dto"
	"github.com/gin-gonic/gin"
)

// ResolveModelRouting freezes routing policy before model ACLs, mapping and
// billing run. The original client spelling is retained only for diagnostics.
func ResolveModelRouting(c *gin.Context, name string) (string, error) {
	base, route, err := dto.ParseModelRouting(name)
	if err != nil {
		return "", err
	}
	if route == nil {
		return name, nil
	}
	constraints := GetChannelConstraints(c)
	constraints.Routing = route
	for _, filter := range route.Filters {
		constraints.AddFilter(dto.ChannelFilter{Kind: dto.FilterMultiplier, Multiplier: filter})
	}
	c.Set("routing_requested_model", name)
	return base, nil
}
