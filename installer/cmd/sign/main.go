// Release signing is a build-time operation; private keys never enter bundles.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
)

func main() {
	generate := flag.String("generate", "", "create a private release key (outside bundle)")
	keyPath := flag.String("key", "", "release private key")
	path := flag.String("manifest", "", "manifest to sign; omit to print public key")
	flag.Parse()
	if *generate != "" {
		_, key, err := ed25519.GenerateKey(rand.Reader)
		check(err)
		f, err := os.OpenFile(*generate, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		check(err)
		_, err = f.WriteString(base64.StdEncoding.EncodeToString(key))
		check(err)
		check(f.Close())
		return
	}
	b, err := os.ReadFile(*keyPath)
	check(err)
	key, err := base64.StdEncoding.DecodeString(string(b))
	check(err)
	if len(key) != ed25519.PrivateKeySize {
		panic("invalid release key")
	}
	if *path == "" {
		fmt.Println(base64.StdEncoding.EncodeToString(ed25519.PrivateKey(key).Public().(ed25519.PublicKey)))
		return
	}
	b, err = os.ReadFile(*path)
	check(err)
	check(os.WriteFile(*path+".sig", []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(key, b))), 0644))
}
func check(e error) {
	if e != nil {
		fmt.Fprintln(os.Stderr, "release signing failed:", e)
		os.Exit(1)
	}
}
