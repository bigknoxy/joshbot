package agent

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/bigknoxy/joshbot/internal/providers"
	"github.com/bigknoxy/joshbot/internal/session"
)

// imageRefs converts this turn's attachments to the descriptors the session
// persists. The bytes are deliberately dropped here: see session.ImageRef for
// why the session stores a record rather than the image.
func imageRefs(images []providers.Image) []session.ImageRef {
	if len(images) == 0 {
		return nil
	}
	refs := make([]session.ImageRef, 0, len(images))
	for _, im := range images {
		sum := sha256.Sum256(im.Data)
		refs = append(refs, session.ImageRef{
			MIME:   im.MIME,
			Bytes:  len(im.Data),
			SHA256: hex.EncodeToString(sum[:]),
		})
	}
	return refs
}

// attachImages puts this turn's images on the last user message of the request.
//
// It targets the last user message rather than appending a new one because the
// caption and the image belong to the same turn — an image in a message of its
// own reads to the model as an unprompted attachment, and on providers that
// require alternating roles it is a 400. If there is no user message (the memory
// window can slide such that none is left), the images are dropped rather than
// forced somewhere they do not belong: the request is then a text request, which
// is wrong but sendable, and the capability gate has already run.
func attachImages(messages []providers.Message, images []providers.Image) {
	if len(images) == 0 {
		return
	}
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == providers.RoleUser {
			messages[i].Images = images
			return
		}
	}
}

// documentRefs converts this turn's document attachments to the descriptors the
// session persists. The bytes are deliberately dropped here: see
// session.DocumentRef for why the session stores a record rather than the file.
func documentRefs(docs []providers.Document) []session.DocumentRef {
	if len(docs) == 0 {
		return nil
	}
	refs := make([]session.DocumentRef, 0, len(docs))
	for _, d := range docs {
		sum := sha256.Sum256(d.Data)
		refs = append(refs, session.DocumentRef{
			Label:  d.Label,
			MIME:   d.MIME,
			Bytes:  len(d.Data),
			SHA256: hex.EncodeToString(sum[:]),
		})
	}
	return refs
}

// attachDocuments puts this turn's documents on the last user message of the
// request, for exactly the reasons attachImages targets that message: the
// caption and the attachment belong to the same turn, and a lone attachment
// message is a 400 on providers that require alternating roles. With no user
// message left in the memory window they are dropped rather than forced
// somewhere they do not belong; the capability gate has already run.
func attachDocuments(messages []providers.Message, docs []providers.Document) {
	if len(docs) == 0 {
		return
	}
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == providers.RoleUser {
			messages[i].Documents = docs
			return
		}
	}
}
