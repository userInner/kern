package app

// RuntimeCapabilities reports optional adapters connected to this process.
// It describes executable reality rather than declarations in a plugin or
// configuration file.
type RuntimeCapabilities struct {
	WritableCredentialStore bool `json:"writable_credential_store"`
	WASMPluginRuntime       bool `json:"wasm_plugin_runtime"`
}

// Capabilities returns the optional production adapters connected at startup.
func (r *Runtime) Capabilities() RuntimeCapabilities {
	if r == nil {
		return RuntimeCapabilities{}
	}
	return RuntimeCapabilities{
		WritableCredentialStore: r.vaultReady,
		WASMPluginRuntime:       r.wasmPluginRuntime,
	}
}
