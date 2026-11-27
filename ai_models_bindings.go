package main

import (
	"context"
	"time"

	"github.com/haoch1/Shell/internal/local"
)

func (a *App) FetchAIModels(cfg local.AIConfigInput) ([]string, error) {
	ctx, cancel := context.WithTimeout(a.baseContext(), 15*time.Second)
	defer cancel()
	return local.FetchAIModels(ctx, cfg)
}
