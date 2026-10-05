package mcptools

import (
	"testing"

	"github.com/draftplane/draftplane/version"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestTheHandshakeReportsADerivedVersion drives a real initialize and reads
// what an agent is actually told, rather than the constant behind it. What it
// guards is re-hardcoding: a literal put back here passes every other test in
// this package.
func TestTheHandshakeReportsADerivedVersion(t *testing.T) {
	ctx := t.Context()
	f := setup(t)
	server := NewServer(New(f.newStore(t), "calm-mountain"))

	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = serverSession.Close() }()

	c := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	clientSession, err := c.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = clientSession.Close() }()

	info := clientSession.InitializeResult().ServerInfo
	if info == nil {
		t.Fatal("the handshake carried no serverInfo at all")
	}
	if info.Name != "draftplane" {
		t.Errorf("serverInfo.Name = %q, want %q", info.Name, "draftplane")
	}
	// Equality with version.Short() rather than a literal: the point is that
	// the handshake DERIVES its version.
	if want := version.Short(); info.Version != want {
		t.Errorf("serverInfo.Version = %q, want version.Short() = %q", info.Version, want)
	}
	if info.Version == "" {
		t.Error("serverInfo.Version is empty; version.Short() is documented never to be")
	}
}
