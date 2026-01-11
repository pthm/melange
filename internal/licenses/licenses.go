package licenses

import (
	_ "embed"
	"strings"
)

//go:generate go run github.com/google/go-licenses@v1.6.0 save ../../cmd/melange --save_path=third_party --force --ignore github.com/pthm/melange
//go:generate go run gen_notice.go

//go:embed assets/LICENSE
var licenseText string

//go:embed assets/THIRD_PARTY_NOTICES
var thirdPartyText string

func LicenseText() string {
	return strings.TrimRight(licenseText, "\n")
}

func ThirdPartyText() string {
	return strings.TrimRight(thirdPartyText, "\n")
}
