package agentbench

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type streamEvent struct {
	Type    string `json:"type"`
	Result  string `json:"result"`
	IsError bool   `json:"is_error"`
	Message struct {
		Content []struct {
			Type string `json:"type"`
			Name string `json:"name"`
		} `json:"content"`
	} `json:"message"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	NumTurns int     `json:"num_turns"`
	CostUSD  float64 `json:"total_cost_usd"`
}

func parseStreamJSON(r io.Reader) (Transcript, error) {
	var transcript Transcript
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var event streamEvent
		if err := json.Unmarshal(line, &event); err != nil {
			continue
		}
		for _, block := range event.Message.Content {
			if block.Type == "tool_use" {
				transcript.ToolCalls++
				if block.Name == "Read" {
					transcript.FilesRead++
				}
			}
		}
		if event.Type == "result" {
			transcript.Output = event.Result
			transcript.InputTokens = event.Usage.InputTokens
			transcript.OutputTokens = event.Usage.OutputTokens
			transcript.Turns = event.NumTurns
			transcript.CostUSD = event.CostUSD
			if event.IsError {
				transcript.AgentError = event.Result
			}
		}
	}
	return transcript, scanner.Err()
}

func closingFormInstruction(task Task) string {
	instruction := "Arbeite frei. Wenn du fertig bist, gib als LETZTES genau einen Block aus:\n\n" +
		formFence + "\n{\"slots\": {"
	for i, slot := range task.Facts {
		if i > 0 {
			instruction += ", "
		}
		instruction += fmt.Sprintf("%q: {%q: ...}", slot.ID, string(slot.Type))
	}
	instruction += "}, \"evidence\": [{\"path\": \"...\", \"lines\": \"...\"}], \"explanation\": \"...\"}\n```\n\n" +
		"Ein Feld, das du nicht sicher beantworten kannst, laesst du weg. " +
		"Raten wird schlechter bewertet als Weglassen."
	return instruction
}

func envSlice(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for key, value := range env {
		out = append(out, key+"="+value)
	}
	sort.Strings(out)
	return out
}

type ClaudeCodeAgent struct {
	binary    string
	workspace Workspace
	rawDir    string
	runtime   Runtime
	redactor  Redactor
}

func NewClaudeCodeAgent(binary string, ws Workspace, rawDir string, runtime Runtime) *ClaudeCodeAgent {
	if runtime == nil {
		runtime = LocalRuntime{}
	}
	return &ClaudeCodeAgent{
		binary: binary, workspace: ws, rawDir: rawDir, runtime: runtime,
		redactor: RedactorFromEnv(ModelCredentialVars...),
	}
}

// WithRedactor replaces the credential redactor. The default already covers
// the model credentials; this exists for a campaign that forwards more.
func (a *ClaudeCodeAgent) WithRedactor(r Redactor) *ClaudeCodeAgent {
	a.redactor = r
	return a
}

func (a *ClaudeCodeAgent) Run(ctx context.Context, inv Invocation) (Transcript, error) {
	if timeout := time.Duration(inv.Config.TimeoutSeconds) * time.Second; timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	argv := []string{
		a.binary,
		"-p", inv.Task.Prompt + "\n\n" + closingFormInstruction(inv.Task),
		"--output-format", "stream-json", "--verbose",
		"--model", inv.Config.ModelID,
		"--permission-mode", inv.Config.PermissionMode,
		"--max-turns", fmt.Sprint(inv.Config.MaxTurns),
	}
	started := time.Now()
	out, err := a.runtime.Command(ctx, a.workspace, inv.Arm, argv).Output()
	// Redigiert wird vor jeder weiteren Verwendung: die Rohausgabe wandert in
	// die veroeffentlichten Transkripte und in Fehlermeldungen.
	out = a.redactor.Bytes(out)
	if err != nil {
		return Transcript{}, errors.New(a.redactor.String(describeExecErrorWithOutput(err, out)))
	}
	transcript, err := parseStreamJSON(bytes.NewReader(out))
	if err != nil {
		return Transcript{}, err
	}
	transcript.Duration = time.Since(started)
	transcript.RawPath, err = writeRaw(a.rawDir, inv, out)
	return transcript, err
}

// describeExecError keeps the process's stderr in the failure message. A bare
// "exit status 1" cannot be told apart from a missing credential, a broken
// image or a timeout — and across hundreds of runs that difference decides
// whether a campaign is repairable or wasted.
// describeExecErrorWithOutput falls back to stdout: Claude Code writes its
// diagnosis into the stream-json on stdout, so a failed run whose stderr is
// empty would otherwise reduce to "exit status 1".
func describeExecErrorWithOutput(err error, stdout []byte) string {
	described := describeExecError(err)
	if strings.Contains(described, ": ") {
		return described
	}
	// Das stream-json traegt die Ursache im result-Feld; nur wenn das fehlt,
	// wird das Rohende angehaengt.
	if transcript, parseErr := parseStreamJSON(bytes.NewReader(stdout)); parseErr == nil && transcript.Output != "" {
		return described + ": " + transcript.Output
	}
	if tail := strings.TrimSpace(string(stdout)); tail != "" {
		const limit = 1500
		if len(tail) > limit {
			tail = tail[len(tail)-limit:]
		}
		return described + ": " + tail
	}
	return described
}

func describeExecError(err error) string {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || len(exitErr.Stderr) == 0 {
		return err.Error()
	}
	stderr := strings.TrimSpace(string(exitErr.Stderr))
	const limit = 1500
	if len(stderr) > limit {
		stderr = stderr[:limit] + " ... (gekuerzt)"
	}
	return err.Error() + ": " + stderr
}

func writeRaw(dir string, inv Invocation, raw []byte) (string, error) {
	if dir == "" {
		return "", nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	repetition := inv.Repetition
	if repetition < 1 {
		repetition = 1
	}
	path := filepath.Join(dir, fmt.Sprintf("%s--%s--r%d.jsonl", inv.Task.ID, inv.Arm, repetition))
	return path, os.WriteFile(path, raw, 0o644)
}
