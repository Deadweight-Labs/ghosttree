package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/aiproxy"
	"github.com/Deadweight-Labs/ghosttree/internal/llm"
	"github.com/Deadweight-Labs/ghosttree/internal/secretbox"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// envAIProxyAddr is where the model proxy listens: a loopback address, by
// default 127.0.0.1 on a free port; "off" switches it off.
const envAIProxyAddr = "GHOSTTREE_AI_PROXY_ADDR"

// fileConnectionName is the server connection llm.json becomes.
const fileConnectionName = "standard"

// aiRuntime is what the server needs of the AI connections (REQ-694): the box
// that seals keys, and the proxy's address for runs.
type aiRuntime struct {
	Box     *secretbox.Box
	BaseURL string
	// EndRun stops the calls of a run still under way (Proxy.EndRun).
	EndRun func(runID int64)
	// Tester makes the test call of "Test connection": the proxy's own way to a
	// provider, also when its listener is off.
	Tester *aiproxy.Proxy
	wait   func()
}

func (a aiRuntime) Wait() {
	if a.wait != nil {
		a.wait()
	}
}

// startAI reads the server key, takes over llm.json as the server connection
// "standard" and starts the model proxy on loopback. A configured server key
// that cannot be read is an error; everything else only switches parts off.
func startAI(ctx context.Context, st *store.Store, logger *slog.Logger) (aiRuntime, error) {
	var rt aiRuntime
	box, err := secretbox.Load()
	if err != nil {
		return rt, err
	}
	rt.Box = box
	if box != nil {
		logger.Info("ai_secret_key", "set", true)
		// Keys sealed before they were bound to their row are sealed again.
		resealed, unusable, err := st.ResealAIKeys(box.Open, box.Seal)
		switch {
		case err != nil:
			logger.Error("ai_keys", "error", err.Error())
		case resealed > 0 || unusable > 0:
			logger.Info("ai_keys", "resealed", resealed, "unusable", unusable)
		}
	} else {
		logger.Info("ai_secret_key", "set", false, "note", "no connection key can be stored without it; see deploy/README.md")
	}
	fileKeys := map[int64]string{}
	if c, key, ok := llmFileConnection(logger); ok {
		conn, err := st.ImportFileAIConnection(c)
		switch {
		case err != nil:
			logger.Warn("ai_llm_json", "error", err.Error())
		default:
			fileKeys[conn.ID] = key
			logger.Info("ai_llm_json", "connection", conn.Name, "model", c.Model)
		}
	}
	p := &aiproxy.Proxy{Store: st, Box: box, FileKeys: fileKeys, Log: logger}
	rt.Tester = p
	addr := strings.TrimSpace(os.Getenv(envAIProxyAddr))
	if addr == "off" {
		logger.Info("ai_proxy", "state", "off")
		return rt, nil
	}
	ln, err := aiproxy.Listen(addr)
	if err != nil {
		return rt, err
	}
	rt.BaseURL, rt.wait = p.Serve(ctx, ln)
	rt.EndRun = p.EndRun
	logger.Info("ai_proxy", "listen", ln.Addr().String())
	return rt, nil
}

// llmFileConnection reads the provider of llm.json the way the server always
// did: the top-level entry, else the gardener's. The key stays in memory.
func llmFileConnection(logger *slog.Logger) (store.AIFileConnection, string, bool) {
	cfg, err := llm.LoadConfig()
	if err != nil || cfg.Format == "" || cfg.APIKey == "" {
		g, gerr := llm.LoadGardenConfig()
		if gerr != nil {
			if !errors.Is(gerr, llm.ErrGardenDisabled) {
				logger.Info("ai_llm_json", "state", "not read", "error", gerr.Error())
			}
			return store.AIFileConnection{}, "", false
		}
		cfg = g.Config
	}
	if err := llm.CheckBaseURL("llm.json", cfg.BaseURL); err != nil {
		logger.Warn("ai_llm_json", "error", err.Error())
		return store.AIFileConnection{}, "", false
	}
	price, _ := cfg.Price()
	return store.AIFileConnection{Name: fileConnectionName, Format: cfg.Format, BaseURL: cfg.BaseURL, Model: cfg.Model,
		InUSD: price.InUSD, OutUSD: price.OutUSD, KeyLast4: secretbox.Last4(cfg.APIKey)}, cfg.APIKey, true
}
