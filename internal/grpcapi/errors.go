package grpcapi

import "openpak/account/internal/identity"

func identityErrBadCredentials() error { return identity.ErrBadCredentials }
func identityErrBanned() error         { return identity.ErrAccountBanned }
func identityErrDeleted() error        { return identity.ErrAccountDeleted }
