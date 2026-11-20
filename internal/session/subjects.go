package session

import (
	"bytes"
	"context"

	"ramus/internal/git/catfile"
	"ramus/internal/git/object"
	"ramus/internal/git/repo"
	"ramus/internal/git/textenc"
)

// LoadSubjects reads the subjects of commits oids off the main thread and
// hands them to done on it, in their order; a commit that cannot be read
// has "". cancel drops the answer, and so does the repository closing;
// with none open, or no commits asked for, done hears nil at once.
func (s *Session) LoadSubjects(oids [][]byte, done func([]string)) (cancel func()) {
	r := s.Repo
	if r == nil || len(oids) == 0 {
		// None read: the caller hears all the same.
		done(nil)
		return func() {}
	}
	ctx, stop := context.WithCancel(s.viewContext(r))
	// dropped is read and written on the main thread only.
	dropped := false
	r.Life().Go(func(context.Context) {
		defer stop()
		out, err := readSubjects(ctx, r, oids)
		if err != nil {
			return
		}
		s.handOver(r, func() {
			if !dropped {
				done(out)
			}
		}, nil)
	})
	return func() { dropped = true; stop() }
}

// ReadSubjects is LoadSubjects for what the user asked for once, as a
// copy, which is not dropped with the view and always hears an answer:
// the subjects, or why there are none; ErrReopened when the repository
// closed and opened again (Trust), for the caller to ask again of the one
// open now.
func (s *Session) ReadSubjects(oids [][]byte, done func([]string, error)) {
	r := s.Repo
	if r == nil {
		done(nil, errNotOpen)
		return
	}
	r.Life().Go(func(ctx context.Context) {
		out, err := readSubjects(ctx, r, oids)
		s.handOver(r, func() { done(out, err) }, func() { done(nil, ErrReopened) })
	})
}

// readSubjects reads the subjects of commits oids in r.
func readSubjects(ctx context.Context, r *repo.Repository, oids [][]byte) ([]string, error) {
	out := make([]string, len(oids))
	cat, err := r.CatFile()
	if err != nil {
		return nil, err
	}
	_ = cat.ReadContext(ctx, oids, func(i int, o catfile.Object) error {
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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
