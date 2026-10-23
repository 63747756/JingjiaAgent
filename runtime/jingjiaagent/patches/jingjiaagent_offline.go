package driver

func jingjiaAgentOfficeEnvironment(env map[string]string) {
	env["NODE_PATH"] = "/opt/office/node/node_modules"
	env["MPLBACKEND"] = "Agg"
	env["OPENBLAS_NUM_THREADS"] = "1"
	env["PIP_DISABLE_PIP_VERSION_CHECK"] = "1"
	env["OPENCODE_DISABLE_AUTOUPDATE"] = "true"
	env["OPENCODE_DISABLE_MODELS_FETCH"] = "1"
	env["OPENCODE_DISABLE_DEFAULT_PLUGINS"] = "true"
	env["DISABLE_AUTOUPDATER"] = "1"
	env["DISABLE_TELEMETRY"] = "1"
	env["DISABLE_ERROR_REPORTING"] = "1"
	env["DISABLE_NONESSENTIAL_TRAFFIC"] = "1"
}
