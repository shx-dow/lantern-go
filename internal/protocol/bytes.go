package protocol

import (
	"encoding/base64"
	"fmt"
	"unicode/utf8"
)

// A filename on Linux is a byte string, and it may carry bytes that are not
// valid UTF-8. encoding/json cannot hold those: it replaces every invalid byte
// with U+FFFD, silently and without error, so a file named report\xff.txt
// arrives as report�.txt and is stored under the wrong name.
//
// This is Linux-only in practice. macOS returns EILSEQ for such a name, so the
// file cannot be created, and Windows stores names as UTF-16 and substitutes
// U+FFFD before the file exists. Only a Linux device can receive such a name,
// so only a Linux device needs this.
//
// Anything larger than a single wrong byte is worse. Two different source files
// whose names differ only in an invalid byte both collapse onto the same
// replacement character, so they become one destination.
//
// The fix is a second field, base64, used only when the plain one cannot carry
// the value. It is additive rather than a change of encoding, so a peer that
// only understands the plain field is no worse off than before: it still reads
// a lossy name, which is what it always did.

// Base64Of returns v as base64 when v is not valid UTF-8, and "" when it is.
//
// The empty string is the sentinel for "nothing to carry", which is safe
// because a base64 encoding of a non-empty input is never empty.
func Base64Of(v string) string {
	if v == "" || utf8.ValidString(v) {
		return ""
	}
	return base64.StdEncoding.EncodeToString([]byte(v))
}

// FromBase64 restores a value carried by Base64Of. field names the companion
// being decoded, so a refusal says which one a peer got wrong rather than
// naming whichever field this call site happened to be written for first.
//
// b64 wins over plain when both are present, because a sender that set it knew
// the plain value was not faithful. The value is still confined by the caller,
// which resolves it against its own roots, so choosing between two spellings
// of the same path cannot widen anything.
func FromBase64(plain, b64, field string) (string, error) {
	if b64 == "" {
		return plain, nil
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", fmt.Errorf("%s is not valid base64: %w", field, err)
	}
	return string(raw), nil
}
