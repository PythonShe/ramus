package session

import (
	"bytes"
	"context"

	"ramus/internal/git/catfile"
	"ramus/internal/git/object"
	"ramus/internal/git/textenc"
)

// LoadSubjects reads the subjects of commits oids off the main thread and
// hands them to done on it, in their order; a commit that cannot be read
// has "". cancel drops the answer.
func (s *Session) LoadSubjects(oids [][]byte, done func([]string)) (cancel func()) {
	r := s.Repo
	if r == nil || len(oids) == 0 {
		return func() {}
	}
	ctx, stop := context.WithCancel(s.viewContext(r))
	// dropped is read and written on the main thread only.
	dropped := false
	r.Life().Go(func(context.Context) {
		defer stop()
		out := make([]string, len(oids))
		cat, err := r.CatFile()
		if err != nil {
			return
		}
		_ = cat.Read(oids, func(i int, o catfile.Object) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if o.Type != "commit" {
				return nil
			}
			cm, err := object.ParseCommit(o.Data)
			if err != nil {
				return nil
			}
			subject, _, _ := bytes.Cut(bytes.TrimLeft(cm.Message, "\n"), []byte("\n"))
			out[i], _ = textenc.Decode(bytes.TrimSpace(subject), string(cm.Encoding))
			return nil
		})
		if ctx.Err() != nil {
			return
		}
		s.update(func() {
			if !dropped {
				done(out)
			}
		})
	})
	return func() { dropped = true; stop() }
}
