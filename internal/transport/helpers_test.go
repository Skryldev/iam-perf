package transport

import (
	"encoding/pem"
	"os"
)

func certToPEM(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func writeFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o600)
}
