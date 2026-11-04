package onedrivefs_test

import (
	"context"
	"fmt"

	"golang.org/x/oauth2/clientcredentials"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/onedrivefs"
)

// ExampleNew connects to the default document library of a SharePoint
// site with the client credentials of an app of Microsoft Entra ID
// and registers it, so that its files can also be used by URI.
//
// The example is compiled but not run, because it needs an app.
func ExampleNew() {
	ctx := context.Background()

	credentials := &clientcredentials.Config{
		ClientID:     "app-client-id",
		ClientSecret: "app-client-secret",
		TokenURL:     "https://login.microsoftonline.com/my-tenant-id/oauth2/v2.0/token",
		Scopes:       []string{"https://graph.microsoft.com/.default"},
	}
	oneDriveFS, err := onedrivefs.New(ctx, "sites/my-site-id/drive", credentials.TokenSource(ctx), nil)
	if err != nil {
		panic(err)
	}
	if err := fs.Register(oneDriveFS); err != nil {
		panic(err)
	}
	defer oneDriveFS.Close() // also unregisters it

	// onedrive://<drive id>/Invoices/2026/invoice-001.pdf
	invoice := oneDriveFS.RootDir().Join("Invoices", "2026", "invoice-001.pdf")
	data, err := invoice.ReadAll(ctx)
	if err != nil {
		panic(err)
	}
	fmt.Println(len(data))
}
