package driver

import "os"

func jingjiaAgentOfficeEnvironment(env map[string]string) {
	env["NODE_PATH"] = "/opt/office/node/node_modules"
	env["MPLBACKEND"] = "Agg"
	env["OPENBLAS_NUM_THREADS"] = "1"
	env["PIP_NO_INDEX"] = "1"
	env["PIP_DISABLE_PIP_VERSION_CHECK"] = "1"
	if os.Getenv("JINGJIAAGENT_OFFLINE") != "1" {
		return
	}
	proxy := os.Getenv("JINGJIAAGENT_OFFLINE_PROXY")
	env["JINGJIAAGENT_OFFLINE"] = "1"
	env["HTTP_PROXY"] = proxy
	env["HTTPS_PROXY"] = proxy
	env["http_proxy"] = proxy
	env["https_proxy"] = proxy
	env["NO_PROXY"] = "localhost,127.0.0.1,::1,backend,runtime,runtime-proxy,storage"
	env["no_proxy"] = env["NO_PROXY"]
	env["OPENCODE_DISABLE_AUTOUPDATE"] = "true"
	env["OPENCODE_DISABLE_MODELS_FETCH"] = "1"
	env["OPENCODE_DISABLE_DEFAULT_PLUGINS"] = "true"
	env["DISABLE_AUTOUPDATER"] = "1"
	env["DISABLE_TELEMETRY"] = "1"
	env["DISABLE_ERROR_REPORTING"] = "1"
	env["DISABLE_NONESSENTIAL_TRAFFIC"] = "1"
}
