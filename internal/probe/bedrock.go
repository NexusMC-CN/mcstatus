package probe

import (
	"context"
	"errors"
	"strings"
	"time"
)

func (s *Service) probeBedrockTransport(ctx context.Context, req Request) (*Result, error) {
	nethernet, raknet := bedrockEndpoints(req)
	if req.BedrockTransport == BedrockTransportNetherNet {
		raknet = nil
	}
	if req.BedrockTransport == BedrockTransportRakNet {
		nethernet = nil
	}
	type resolution struct {
		target pinnedTarget
		err    error
	}
	targets := make(map[string]resolution)
	resolve := func(stageCtx context.Context, endpoint *Endpoint) (pinnedTarget, error) {
		// Resolve only an attempted stage, and pin one validated snapshot per host.
		// Cache failures too: another protocol must not re-resolve an unsafe host.
		key := strings.ToLower(strings.TrimSuffix(endpoint.Host, "."))
		cached, ok := targets[key]
		if !ok {
			cached.target, cached.err = s.resolveTarget(stageCtx, endpoint.Host, endpoint.Port)
			targets[key] = cached
		}
		target := cached.target
		target.Host, target.Port = endpoint.Host, endpoint.Port
		return target, cached.err
	}
	var failures []error
	var resolutionFailures []error
	if nethernet != nil {
		probeCtx := ctx
		cancel := func() {}
		if raknet != nil {
			deadline, _ := ctx.Deadline()
			budget := time.Until(deadline) / 2
			if budget > 1500*time.Millisecond {
				budget = 1500 * time.Millisecond
			}
			probeCtx, cancel = context.WithTimeout(ctx, budget)
		}
		target, err := resolve(probeCtx, nethernet)
		var result *Result
		if err != nil {
			resolutionFailures = append(resolutionFailures, err)
		} else {
			result, err = s.probeNetherNet(probeCtx, req, target, nethernet.Scheme)
		}
		cancel()
		if err == nil {
			return result, nil
		}
		failures = append(failures, err)
	}
	if raknet != nil && ctx.Err() == nil {
		target, err := resolve(ctx, raknet)
		var result *Result
		if err != nil {
			resolutionFailures = append(resolutionFailures, err)
		} else {
			result, err = s.probeRakNet(ctx, req, target)
		}
		if err == nil {
			return result, nil
		}
		failures = append(failures, err)
	}
	if len(resolutionFailures) > 0 {
		return nil, errors.Join(resolutionFailures...)
	}
	if ctx.Err() != nil {
		failures = append(failures, ctx.Err())
	}
	if len(failures) == 0 {
		failures = append(failures, errors.New("no protocol attempted"))
	}
	return buildErrorResult(req, classifyProbeError(failures...)), nil
}
