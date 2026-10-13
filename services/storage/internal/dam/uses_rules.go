package dam

import "fmt"

// checkUse validates a (project, ref) pair for every UseStore.
func checkUse(projectID string, ref AssetRef) error {
	if err := checkProjectID(projectID); err != nil {
		return err
	}
	if err := ref.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidQuery, err)
	}
	return nil
}

func checkProjectID(projectID string) error {
	if projectID == "" {
		return fmt.Errorf("%w: empty project ID", ErrInvalidQuery)
	}
	return nil
}
