// Package plugin defines the experimental, versioned message protocol between the
// Renvoy core and external provider plugins.
//
// A plugin is an executable named renvoy-provider-<name>. The core starts it, writes
// one JSON request per line to its stdin, and reads one JSON response per line from
// its stdout. Plugins never receive secret values: pipelines only carry secret names.
//
// The protocol is experimental while the configuration API is v1alpha1. Breaking
// changes bump ProtocolVersion. See docs/plugins.md.
package plugin

import "encoding/json"

// ProtocolVersion is the protocol version this package describes.
const ProtocolVersion = "renvoy.dev/plugin/v1alpha1"

// Message types.
const (
	TypeHandshake    = "handshake"
	TypeCapabilities = "capabilities"
	TypeValidate     = "validate"
	TypeRender       = "render"
	TypeError        = "error"
)

// Request is a message from the core to a plugin.
type Request struct {
	// ID correlates a response with its request.
	ID string `json:"id"`
	// Type selects the operation.
	Type string `json:"type"`
	// Handshake is set for TypeHandshake.
	Handshake *HandshakeRequest `json:"handshake,omitempty"`
	// Pipeline is the normalized pipeline for TypeValidate and TypeRender.
	Pipeline json.RawMessage `json:"pipeline,omitempty"`
	// Render carries options for TypeRender.
	Render *RenderOptions `json:"render,omitempty"`
}

// Response is a message from a plugin to the core.
type Response struct {
	ID           string             `json:"id"`
	Type         string             `json:"type"`
	Handshake    *HandshakeResponse `json:"handshake,omitempty"`
	Capabilities []Capability       `json:"capabilities,omitempty"`
	Diagnostics  []Diagnostic       `json:"diagnostics,omitempty"`
	Files        []File             `json:"files,omitempty"`
	Error        *Error             `json:"error,omitempty"`
}

// HandshakeRequest opens a session and lists the protocol versions the core speaks.
type HandshakeRequest struct {
	CoreVersion      string   `json:"coreVersion"`
	ProtocolVersions []string `json:"protocolVersions"`
	ConfigAPIVersion string   `json:"configApiVersion"`
}

// HandshakeResponse selects one protocol version and describes the plugin.
type HandshakeResponse struct {
	ProtocolVersion string `json:"protocolVersion"`
	Name            string `json:"name"`
	DisplayName     string `json:"displayName"`
	Description     string `json:"description"`
	PluginVersion   string `json:"pluginVersion"`
}

// Capability is one row of the plugin's capability matrix.
type Capability struct {
	ID      string `json:"id"`
	Support string `json:"support"` // native, emulated, provider-managed, unsupported
	Note    string `json:"note,omitempty"`
}

// Diagnostic mirrors Renvoy's structured diagnostics.
type Diagnostic struct {
	Severity string `json:"severity"` // error, warning, info
	Path     string `json:"path,omitempty"`
	Message  string `json:"message"`
	Hint     string `json:"hint,omitempty"`
}

// RenderOptions configures a render request.
type RenderOptions struct {
	SourceName string `json:"sourceName"`
}

// File is one generated file. The core adds headers and checksums, preserves
// custom blocks, and decides whether it may be written.
type File struct {
	// Path is relative, uses forward slashes, and must not escape the output directory.
	Path string `json:"path"`
	// Content is the file body as UTF-8 text.
	Content string `json:"content"`
	// Comment is the line comment prefix used for the generated header.
	Comment string `json:"comment"`
	// CommentEnd closes header comment lines for formats without line comments.
	CommentEnd string `json:"commentEnd,omitempty"`
}

// Error reports a failed request.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
