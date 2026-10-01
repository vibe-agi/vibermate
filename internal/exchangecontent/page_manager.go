package exchangecontent

import "context"

func (manager *Manager) GetPagedProjection(ctx context.Context, exchangeID string, view RequestView) (Projection, error) {
	if !validIdentity(exchangeID, MaxExchangeIDBytes) || (view != RequestViewFull && view != RequestViewIncremental) {
		return Projection{}, ErrInvalidEvidence
	}
	operation, finish, err := manager.begin(ctx)
	if err != nil {
		return Projection{}, err
	}
	defer finish()
	value, err := manager.repository.GetPagedProjection(operation, exchangeID, manager.clock.Now().UTC(), view)
	if err != nil {
		return Projection{}, err
	}
	if value.Page == nil || value.Validate() != nil {
		return Projection{}, ErrInvalidEvidence
	}
	return value.Clone(), nil
}

func (manager *Manager) GetContentPage(ctx context.Context, exchangeID, cursor string) (ContentPage, error) {
	if !validIdentity(exchangeID, MaxExchangeIDBytes) || cursor == "" || !validPageCursor(cursor) {
		return ContentPage{}, ErrInvalidEvidence
	}
	operation, finish, err := manager.begin(ctx)
	if err != nil {
		return ContentPage{}, err
	}
	defer finish()
	page, err := manager.repository.GetContentPage(operation, exchangeID, manager.clock.Now().UTC(), cursor)
	if err != nil {
		return ContentPage{}, err
	}
	if page.ExchangeID != exchangeID || page.Validate() != nil {
		return ContentPage{}, ErrInvalidEvidence
	}
	return page, nil
}
