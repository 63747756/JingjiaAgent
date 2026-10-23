package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Keep immutable task payloads and shared image catalog names unchanged. Only
// the restored local node translates references proven by the image archives.
// Remote nodes retain their own image store identity. Run before API startup.
func migrateRecoveredImages(s *deployment) error {
	b, err := os.ReadFile(filepath.Join(s.Root, "recovered-images.json"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var mapping map[string]string
	if err = json.Unmarshal(b, &mapping); err != nil {
		return err
	}
	if len(mapping) == 0 {
		return nil
	}
	for _, id := range mapping {
		if !validImageID(id) {
			return errors.New("invalid recovered image mapping")
		}
	}
	key, err := os.ReadFile(filepath.Join(s.Root, "secrets/payload.key"))
	if err != nil {
		return err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	b, err = deploymentSQL(s, `SELECT COALESCE(json_agg(json_build_object('id',node_id,'payload',encode(payload,'hex'))),'[]'::json) FROM runtime_node_configs WHERE endpoint='https://runtime-proxy:7411'`)
	if err != nil {
		return err
	}
	var rows []struct{ ID, Payload string }
	if err = json.Unmarshal(b, &rows); err != nil {
		return err
	}
	sql := "BEGIN; SELECT pg_advisory_xact_lock(707326030);\n"
	for _, row := range rows {
		raw, err := hex.DecodeString(row.Payload)
		if err != nil {
			return err
		}
		payload, err := remapNodeImages(aead, row.ID, raw, mapping)
		if err != nil {
			return err
		}
		// Values are hex encoded, including identifiers; no plaintext credentials
		// or user-controlled SQL syntax enter the command.
		sql += fmt.Sprintf("UPDATE runtime_node_configs SET payload=decode('%s','hex'),revision=revision+1 WHERE node_id=convert_from(decode('%s','hex'),'UTF8') AND endpoint='https://runtime-proxy:7411';\n", hex.EncodeToString(payload), hex.EncodeToString([]byte(row.ID)))
	}
	_, err = deploymentSQL(s, sql+"COMMIT;")
	return err
}

func remapNodeImages(aead cipher.AEAD, id string, raw []byte, mapping map[string]string) ([]byte, error) {
	n := aead.NonceSize()
	if len(raw) < n {
		return nil, errors.New("invalid local node payload")
	}
	plain, err := aead.Open(nil, raw[:n], raw[n:], []byte("runtime-node:"+id))
	if err != nil {
		return nil, errors.New("cannot decrypt restored local node")
	}
	var value, cfg map[string]json.RawMessage
	if err = json.Unmarshal(plain, &value); err != nil {
		return nil, err
	}
	if err = json.Unmarshal(value["Config"], &cfg); err != nil || cfg == nil {
		return nil, errors.New("invalid restored local node config")
	}
	merged := map[string]string{}
	if cfg["recovered_images"] != nil {
		if err = json.Unmarshal(cfg["recovered_images"], &merged); err != nil {
			return nil, err
		}
	}
	if merged == nil {
		merged = map[string]string{}
	}
	for ref, old := range merged {
		if current := mapping[old]; current != "" {
			merged[ref] = current
		}
	}
	for ref, current := range mapping {
		// Tags are restored directly and may intentionally be updated in future.
		// Only immutable IDs/digests need persistent translation.
		if strings.HasPrefix(ref, "sha256:") || strings.Contains(ref, "@sha256:") {
			merged[ref] = current
		}
	}
	var guest, installed string
	json.Unmarshal(cfg["guest_image"], &guest)
	json.Unmarshal(cfg["installed_guest_image"], &installed)
	if installed != "" {
		guest = installed
	}
	if target := merged[guest]; target != "" {
		cfg["installed_guest_image"], _ = json.Marshal(target)
	}
	cfg["recovered_images"], err = json.Marshal(merged)
	if err != nil {
		return nil, err
	}
	value["Config"], err = json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	plain, err = json.Marshal(value)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, n)
	if _, err = rand.Read(nonce); err != nil {
		return nil, err
	}
	return aead.Seal(nonce, nonce, plain, []byte("runtime-node:"+id)), nil
}
