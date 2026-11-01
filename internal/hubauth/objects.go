package hubauth

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

const objectPrefix = "hub-auth-snapshots/"
const maxObjectBytes = 32 << 20

var ErrObjectMissing = errors.New("hub snapshot object missing")

// BlobStore has no delete operation. It is implemented by B2 and by fixture stores.
type BlobStore interface {
	Get(context.Context, string) ([]byte, error)
	Put(context.Context, string, []byte) error
}

type s3Blobs struct {
	client *minio.Client
	bucket string
}

// NewB2 uses the existing dedicated private prefix binding, never the backup key.
// Prefix permissions are established by t12900's proof before installation.
func NewB2(endpoint, bucket, idFile, keyFile string) (BlobStore, error) {
	id, err := readSecret(idFile)
	if err != nil {
		return nil, err
	}
	key, err := readSecret(keyFile)
	if err != nil {
		return nil, err
	}
	if bucket == "" {
		return nil, errors.New("hub snapshot bucket missing")
	}
	client, err := minio.New(endpoint, &minio.Options{Creds: credentials.NewStaticV4(id, key, ""), Secure: true, Region: "eu-central-003"})
	if err != nil {
		return nil, errors.New("hub snapshot store configuration invalid")
	}
	return &s3Blobs{client: client, bucket: bucket}, nil
}

func validKey(key string) bool {
	var id string
	if strings.HasPrefix(key, objectPrefix+"heads/") {
		id = strings.TrimPrefix(key, objectPrefix+"heads/")
	}
	if strings.HasPrefix(key, objectPrefix+"results/") {
		id = strings.TrimPrefix(key, objectPrefix+"results/")
	}
	u, err := uuid.Parse(id)
	return err == nil && u.Version() == 4 && u.String() == id
}

func (s *s3Blobs) Get(ctx context.Context, key string) ([]byte, error) {
	if !validKey(key) {
		return nil, errors.New("hub snapshot key invalid")
	}
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, errors.New("hub snapshot read failed")
	}
	defer obj.Close()
	data, err := io.ReadAll(io.LimitReader(obj, maxObjectBytes+1))
	if err != nil {
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			return nil, ErrObjectMissing
		}
		return nil, errors.New("hub snapshot read failed")
	}
	if len(data) > maxObjectBytes {
		return nil, errors.New("hub snapshot exceeds size limit")
	}
	return data, nil
}

func (s *s3Blobs) Put(ctx context.Context, key string, data []byte) error {
	if !validKey(key) || len(data) > maxObjectBytes {
		return errors.New("hub snapshot write invalid")
	}
	_, err := s.client.PutObject(ctx, s.bucket, key, bytes.NewReader(data), int64(len(data)), minio.PutObjectOptions{ContentType: "application/octet-stream", DisableMultipart: true})
	if err != nil {
		return errors.New("hub snapshot write failed; outcome unknown")
	}
	return nil
}

type Objects struct {
	blobs BlobStore
	aead  cipher.AEAD
	spool string
}

// NewObjects preserves encrypted result bytes across retries and process restart.
// The spool contains ciphertext only. Keep it with the newest auth material on rollback.
func NewObjects(blobs BlobStore, keyFile, spool string) (*Objects, error) {
	encoded, err := readSecret(keyFile)
	if err != nil {
		return nil, err
	}
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(key) != 32 {
		return nil, errors.New("hub snapshot encryption key invalid")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, errors.New("hub snapshot cipher unavailable")
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(spool) {
		return nil, errors.New("hub snapshot spool must be absolute")
	}
	if err = os.MkdirAll(spool, 0700); err != nil {
		return nil, errors.New("hub snapshot spool unavailable")
	}
	info, err := os.Lstat(spool)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("hub snapshot spool must be a private directory")
	}
	return &Objects{blobs: blobs, aead: aead, spool: spool}, nil
}

func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

func (o *Objects) decrypt(key string, data []byte) ([]byte, error) {
	if !validKey(key) || len(data) < 4+o.aead.NonceSize()+o.aead.Overhead() || len(data) > maxObjectBytes || string(data[:4]) != "HSN1" {
		return nil, errors.New("hub snapshot format invalid")
	}
	n := o.aead.NonceSize()
	plain, err := o.aead.Open(nil, data[4:4+n], data[4+n:], []byte(key))
	if err != nil {
		return nil, errors.New("hub snapshot authentication failed")
	}
	return plain, nil
}

func (o *Objects) Read(ctx context.Context, p Pointer) ([]byte, error) {
	if !validKey(p.ObjectKey) || len(p.SHA256) != 64 {
		return nil, errors.New("hub snapshot pointer invalid")
	}
	data, err := o.blobs.Get(ctx, p.ObjectKey)
	if err != nil {
		return nil, err
	}
	if digest(data) != p.SHA256 {
		return nil, errors.New("hub snapshot digest mismatch")
	}
	return o.decrypt(p.ObjectKey, data)
}

func (o *Objects) ReadResult(ctx context.Context, key string) ([]byte, string, error) {
	data, err := o.blobs.Get(ctx, key)
	if err != nil {
		return nil, "", err
	}
	plain, err := o.decrypt(key, data)
	return plain, digest(data), err
}

func (o *Objects) spoolPath(key string) string {
	return filepath.Join(o.spool, strings.ReplaceAll(strings.TrimPrefix(key, objectPrefix), "/", "-"))
}

// Publish is write-once by object name. A conflicting remote or local object is
// an error, including after an upload loses its reply. No failure deletes bytes.
func (o *Objects) Publish(ctx context.Context, key string, plain []byte) (Pointer, error) {
	p := Pointer{ObjectKey: key}
	if !validKey(key) || len(plain) > maxObjectBytes-64 {
		return p, errors.New("hub snapshot payload invalid")
	}
	path := o.spoolPath(key)
	data, err := readSpool(path)
	if errors.Is(err, os.ErrNotExist) {
		nonce := make([]byte, o.aead.NonceSize())
		if _, err = rand.Read(nonce); err != nil {
			return p, err
		}
		data = append(append([]byte("HSN1"), nonce...), o.aead.Seal(nil, nonce, plain, []byte(key))...)
		f, errOpen := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if errOpen != nil {
			return p, errors.New("hub snapshot spool creation failed")
		}
		_, errWrite := f.Write(data)
		errSync := f.Sync()
		errClose := f.Close()
		if errWrite != nil || errSync != nil || errClose != nil {
			return p, errors.New("hub snapshot spool persist failed")
		}
		dir, errOpenDir := os.Open(o.spool)
		if errOpenDir != nil {
			return p, errors.New("hub snapshot spool sync failed")
		}
		errSync = dir.Sync()
		_ = dir.Close()
		if errSync != nil {
			return p, errors.New("hub snapshot spool sync failed")
		}
	} else if err != nil {
		return p, errors.New("hub snapshot spool read failed")
	}
	existingPlain, err := o.decrypt(key, data)
	if err != nil || !bytes.Equal(existingPlain, plain) {
		return p, errors.New("hub snapshot name already binds different content")
	}
	p.SHA256 = digest(data)
	if err = o.publishBytes(ctx, key, data); err != nil {
		return p, err
	}
	return p, nil
}

func (o *Objects) publishBytes(ctx context.Context, key string, data []byte) error {
	existing, err := o.blobs.Get(ctx, key)
	if err == nil {
		if !bytes.Equal(existing, data) {
			return errors.New("hub snapshot immutable object conflict")
		}
		return nil
	}
	if !errors.Is(err, ErrObjectMissing) {
		return err
	}
	// A retry always sends these exact bytes. Readback resolves a lost upload reply.
	writeErr := o.blobs.Put(ctx, key, data)
	readback, readErr := o.blobs.Get(ctx, key)
	if readErr == nil && bytes.Equal(readback, data) {
		return nil
	}
	if writeErr != nil {
		return writeErr
	}
	return errors.New("hub snapshot readback failed")
}

// RepublishResult recovers a locally persisted result after an object-store outage.
func (o *Objects) RepublishResult(ctx context.Context, intentID string) error {
	key := objectPrefix + "results/" + intentID
	if !validKey(key) {
		return errors.New("hub result key invalid")
	}
	data, err := readSpool(o.spoolPath(key))
	if errors.Is(err, os.ErrNotExist) {
		return ErrObjectMissing
	}
	if err != nil {
		return errors.New("hub result spool unreadable")
	}
	if _, err = o.decrypt(key, data); err != nil {
		return err
	}
	return o.publishBytes(ctx, key, data)
}

func readSpool(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > maxObjectBytes {
		return nil, errors.New("hub spool must be a private regular file")
	}
	return os.ReadFile(path)
}
