package storebench

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"time"
)

func Generate(seed uint64, scale Scale) (Workload, Expected) {
	workload, expected, err := GenerateChecked(seed, scale)
	if err != nil {
		panic(err)
	}
	return workload, expected
}

func GenerateChecked(seed uint64, scale Scale) (Workload, Expected, error) {
	if err := validateScale(scale); err != nil {
		return Workload{}, Expected{}, err
	}
	if scale.ChunkBodyBytes == 0 {
		scale.ChunkBodyBytes = 128
	}
	if scale.DocumentRevisions == 0 && scale.Documents > 0 {
		scale.DocumentRevisions = 1
	}
	g := generator{
		rand: rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)),
		expected: Expected{
			Sessions: map[string]ExpectedSession{}, Ghosts: map[string]ExpectedGhost{},
			Documents: map[string]ExpectedDocument{}, Migrations: map[string]ExpectedMigration{},
		},
	}
	for projectNumber := range scale.Projects {
		g.project(projectNumber, scale)
	}
	return Workload{Seed: seed, Scale: scale, Operations: g.operations}, g.expected, nil
}

func validateScale(scale Scale) error {
	if scale.Projects <= 0 {
		return fmt.Errorf("projects must be positive")
	}
	values := []struct {
		name  string
		value int
	}{
		{"sessions", scale.Sessions}, {"chunks per session", scale.ChunksPerSession},
		{"chunk body bytes", scale.ChunkBodyBytes}, {"ghost files", scale.GhostFiles},
		{"ghost body bytes", scale.GhostBodyBytes}, {"documents", scale.Documents},
		{"document revisions", scale.DocumentRevisions}, {"document body bytes", scale.DocumentBodyBytes},
		{"migration artifacts", scale.MigrationArtifacts}, {"read every", scale.ReadEvery},
	}
	for _, value := range values {
		if value.value < 0 {
			return fmt.Errorf("%s must not be negative", value.name)
		}
	}
	return nil
}

type generator struct {
	rand       *rand.Rand
	operations []Operation
	expected   Expected
}

func (g *generator) project(number int, scale Scale) {
	project := fmt.Sprintf("github.com/bench/project-%02d", number)
	g.sessions(project, number, scale)
	g.ghosts(project, number, scale)
	g.documents(project, number, scale)
	g.migration(project, number, scale)
}

func (g *generator) sessions(project string, projectNumber int, scale Scale) {
	for n := range scale.Sessions {
		logicalID := fmt.Sprintf("session-%02d-%06d", projectNumber, n)
		createID := g.add(SessionUpsert, 0, nil, SessionUpsertPayload{
			LogicalID: logicalID, ExternalID: "bench-" + logicalID, Project: project,
		})
		chunks := make([]ChunkPayload, 0, scale.ChunksPerSession)
		var payloadBytes int64
		for seq := range scale.ChunksPerSession {
			text := g.body(scale.ChunkBodyBytes)
			rawBytes, _ := json.Marshal(struct {
				Seq  int    `json:"seq"`
				Text string `json:"text"`
			}{Seq: seq, Text: text})
			chunk := ChunkPayload{Seq: seq, Role: "user", Text: text, Raw: string(rawBytes)}
			chunks = append(chunks, chunk)
			payloadBytes += int64(len(chunk.Text) + len(chunk.Raw))
		}
		appendID := g.add(ChunksAppend, payloadBytes, []string{createID}, ChunksAppendPayload{Session: logicalID, Chunks: chunks})
		g.add(SessionRead, 0, []string{appendID}, SessionReadPayload{Session: logicalID})
		indexed := make(map[int]ChunkPayload, len(chunks))
		for _, chunk := range chunks {
			indexed[chunk.Seq] = chunk
		}
		g.expected.Sessions[logicalID] = ExpectedSession{ExternalID: "bench-" + logicalID, Project: project, Chunks: indexed}
	}
}

func (g *generator) ghosts(project string, projectNumber int, scale Scale) {
	last := ""
	for n := range scale.GhostFiles {
		path := fmt.Sprintf("packages/pkg-%04d/internal/file-%06d.go", n/100, n)
		description := g.body(scale.GhostBodyBytes)
		payload := GhostPutPayload{Project: project, Path: path, Description: description,
			ContentSHA: digest(description), LineCount: 1 + len(description)/80}
		last = g.add(GhostPut, int64(len(description)), nil, payload)
		g.expected.Ghosts[project+"\x00"+path] = ExpectedGhost{Project: project, Path: path,
			DescriptionDigest: digest(description), DescriptionBytes: len(description)}
		if scale.ReadEvery > 0 && (n+1)%scale.ReadEvery == 0 {
			g.add(GhostRead, 0, []string{last}, GhostReadPayload{Project: project, Path: path})
		}
	}
	if last != "" {
		g.add(GhostTree, 0, []string{last}, GhostTreePayload{Project: project, Prefix: "packages"})
	}
	_ = projectNumber
}

func (g *generator) documents(project string, projectNumber int, scale Scale) {
	for n := range scale.Documents {
		logicalID := fmt.Sprintf("document-%02d-%06d", projectNumber, n)
		body := g.body(scale.DocumentBodyBytes)
		createID := g.add(DocumentCreate, int64(len(body)), nil, DocumentCreatePayload{
			LogicalID: logicalID, Project: project, Slug: logicalID, Title: "Benchmark " + logicalID, Body: body,
		})
		digests := []string{digest(body)}
		previous := createID
		for revision := 2; revision <= scale.DocumentRevisions; revision++ {
			body = g.body(scale.DocumentBodyBytes)
			previous = g.add(DocumentPush, int64(len(body)), []string{previous}, DocumentPushPayload{
				Document: logicalID, Base: revision - 1, Body: body,
			})
			digests = append(digests, digest(body))
		}
		g.add(DocumentRead, 0, []string{previous}, DocumentReadPayload{Document: logicalID, Revision: len(digests)})
		g.expected.Documents[logicalID] = ExpectedDocument{Project: project, Slug: logicalID, RevisionDigests: digests}
	}
}

func (g *generator) migration(project string, projectNumber int, scale Scale) {
	if scale.MigrationArtifacts == 0 {
		return
	}
	logicalID := fmt.Sprintf("migration-%02d", projectNumber)
	artifacts := make([]Artifact, 0, scale.MigrationArtifacts)
	for n := range scale.MigrationArtifacts {
		path := fmt.Sprintf("legacy/docs/%06d.md", n)
		artifacts = append(artifacts, Artifact{Path: path, Digest: digest(g.body(64))})
	}
	beginID := g.add(MigrationBegin, int64(scale.MigrationArtifacts*64), nil, MigrationBeginPayload{
		LogicalID: logicalID, Project: project, Artifacts: artifacts,
	})
	g.add(MigrationRead, 0, []string{beginID}, MigrationReadPayload{Project: project})
	g.expected.Migrations[logicalID] = ExpectedMigration{Project: project, Artifacts: artifacts}
}

func (g *generator) add(kind Kind, payloadBytes int64, dependencies []string, payload any) string {
	id := fmt.Sprintf("op-%08d", len(g.operations)+1)
	g.operations = append(g.operations, Operation{ID: id, Kind: kind, DependsOn: dependencies,
		At: time.Duration(len(g.operations)) * 100 * time.Microsecond, PayloadBytes: payloadBytes, Payload: payload})
	return id
}

func (g *generator) body(size int) string {
	if size <= 0 {
		return ""
	}
	out := make([]byte, size)
	for offset := 0; offset < len(out); {
		word := fmt.Sprintf("%016x", g.rand.Uint64())
		offset += copy(out[offset:], word)
	}
	return string(out)
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
