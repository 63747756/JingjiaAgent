package config

// Work configures the work product with bundled offline dependencies. Enablement is explicit so existing
// developer installations do not silently change their storage contract.
type Work struct {
	Enabled            bool     `mapstructure:"enabled"`
	ConverterURL       string   `mapstructure:"converter_url"`
	ConverterTokenFile string   `mapstructure:"converter_token_file"`
	MaxFileBytes       int64    `mapstructure:"max_file_bytes"`
	MaxArchiveBytes    int64    `mapstructure:"max_archive_bytes"`
	MaxFiles           int      `mapstructure:"max_files"`
	SkillHub           SkillHub `mapstructure:"skillhub"`
}

// SkillHub connects only to the administrator-configured registry.
type SkillHub struct {
	URL           string   `mapstructure:"url"`
	PublicURL     string   `mapstructure:"public_url"`
	Namespace     string   `mapstructure:"namespace"`
	InstanceID    string   `mapstructure:"instance_id"`
	SecretKeyFile string   `mapstructure:"secret_key_file"`
	AllowHTTP     bool     `mapstructure:"allow_http"`
	DownloadHosts []string `mapstructure:"download_hosts"`
}

func (w Work) FileLimit() int64 {
	if w.MaxFileBytes > 0 {
		return w.MaxFileBytes
	}
	return 100 << 20
}
func (w Work) ArchiveLimit() int64 {
	if w.MaxArchiveBytes > 0 {
		return w.MaxArchiveBytes
	}
	return 256 << 20
}
func (w Work) FileCountLimit() int {
	if w.MaxFiles > 0 {
		return w.MaxFiles
	}
	return 2000
}
