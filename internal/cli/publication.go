package cli

import (
	"context"

	"github.com/fissible/hapax/internal/workflow"
)

type publicationState uint8

const (
	publicationNotAdmitted publicationState = iota
	publicationFailed
	publicationEvidenceFailed
	publicationComplete
)

type publicationResult struct {
	state publicationState
	err   error
}

// publishAndRecord owns the cancellation admission boundary. Once admitted,
// caller cancellation cannot abandon publication or its required evidence.
// Disk and database errors still end the operation; these writes are not atomic.
func publishAndRecord(ctx context.Context, publisher Publisher, service workflow.Service, action publicationAction, source, destination string, content []byte, evidence workflow.Publication) publicationResult {
	if err := ctx.Err(); err != nil {
		return publicationResult{state: publicationNotAdmitted, err: err}
	}
	var err error
	switch action {
	case create:
		err = publisher.Create(source, destination, content)
	case replace:
		err = publisher.Replace(source, content)
	case noPublication:
		return publicationResult{state: publicationComplete}
	}
	if err != nil {
		return publicationResult{state: publicationFailed, err: err}
	}
	if len(evidence.Paragraphs) != 0 {
		if err := service.RecordPublication(context.WithoutCancel(ctx), evidence); err != nil {
			return publicationResult{state: publicationEvidenceFailed, err: err}
		}
	}
	return publicationResult{state: publicationComplete}
}
