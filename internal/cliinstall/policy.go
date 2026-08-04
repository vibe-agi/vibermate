// Package cliinstall owns the narrow policy for exposing the CLI packaged in
// VibeMate Desktop as a terminal command. Platform installers keep ownership
// everywhere except a receipt-owned macOS application link.
package cliinstall

import "fmt"

// Platform is an operating-system family with an explicit CLI delivery policy.
type Platform string

const (
	PlatformDarwin  Platform = "darwin"
	PlatformWindows Platform = "windows"
	PlatformLinux   Platform = "linux"
)

// PackageShape identifies the platform package that owns installation.
type PackageShape string

const (
	ShapeMacApp        PackageShape = "mac_app"
	ShapeWindowsNSIS   PackageShape = "windows_nsis"
	ShapeWindowsMSI    PackageShape = "windows_msi"
	ShapeLinuxDeb      PackageShape = "linux_deb"
	ShapeLinuxRPM      PackageShape = "linux_rpm"
	ShapeLinuxAppImage PackageShape = "linux_appimage"
	ShapePortable      PackageShape = "portable"
)

// Owner names the authority allowed to create and remove the terminal entry.
type Owner string

const (
	OwnerDesktopApp     Owner = "desktop_app"
	OwnerInstaller      Owner = "installer"
	OwnerPackageManager Owner = "package_manager"
	OwnerNone           Owner = "none"
)

// Method describes how a packaged CLI becomes terminal-discoverable.
type Method string

const (
	MethodManagedSymlink Method = "managed_symlink"
	MethodInstallerPath  Method = "installer_path"
	MethodPackageBinary  Method = "package_binary"
	MethodAbsoluteOnly   Method = "absolute_only"
)

// Strategy is the immutable ownership decision for a platform package.
type Strategy struct {
	Platform          Platform
	Shape             PackageShape
	Owner             Owner
	Method            Method
	RuntimeMutation   bool
	RequiresStableApp bool
	EditsShellProfile bool
	Notes             string
}

// ResolveStrategy freezes the supported ownership model. The desktop app
// never edits a shell startup file and never emulates an installer or package
// manager on another platform.
func ResolveStrategy(platform Platform, shape PackageShape) (Strategy, error) {
	strategy := Strategy{
		Platform: platform,
		Shape:    shape,
	}
	switch {
	case platform == PlatformDarwin && shape == ShapeMacApp:
		strategy.Owner = OwnerDesktopApp
		strategy.Method = MethodManagedSymlink
		strategy.RuntimeMutation = true
		strategy.RequiresStableApp = true
		strategy.Notes = "create one receipt-owned terminal link to the signed CLI inside a stable app bundle"
	case platform == PlatformWindows &&
		(shape == ShapeWindowsNSIS || shape == ShapeWindowsMSI):
		strategy.Owner = OwnerInstaller
		strategy.Method = MethodInstallerPath
		strategy.Notes = "the installer owns the CLI file and terminal PATH registration; the running app only inspects it"
	case platform == PlatformLinux &&
		(shape == ShapeLinuxDeb || shape == ShapeLinuxRPM):
		strategy.Owner = OwnerPackageManager
		strategy.Method = MethodPackageBinary
		strategy.Notes = "the package installs and removes /usr/bin/vibermate with its manifest"
	case platform == PlatformLinux && shape == ShapeLinuxAppImage:
		strategy.Owner = OwnerNone
		strategy.Method = MethodAbsoluteOnly
		strategy.Notes = "AppImage has no stable mounted sidecar path; use an absolute command or install a separate packaged CLI"
	case shape == ShapePortable:
		strategy.Owner = OwnerNone
		strategy.Method = MethodAbsoluteOnly
		strategy.Notes = "portable builds do not mutate PATH"
	default:
		return Strategy{}, fmt.Errorf(
			"unsupported CLI delivery combination %s/%s",
			platform,
			shape,
		)
	}
	return strategy, nil
}
