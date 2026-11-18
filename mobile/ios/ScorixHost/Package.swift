// swift-tools-version:5.9
import PackageDescription

let package = Package(
    name: "ScorixHost",
    platforms: [.iOS(.v16), .macOS(.v13)],
    products: [
        .library(name: "ScorixHost", targets: ["ScorixHost"]),
        .library(name: "ScorixWidgets", targets: ["ScorixWidgets"]),
    ],
    targets: [
        .target(name: "ScorixGo"),
        .target(name: "ScorixHost", dependencies: ["ScorixGo"]),
        .target(name: "ScorixWidgets"),
        .testTarget(name: "ScorixHostTests", dependencies: ["ScorixHost", "ScorixWidgets"]),
    ]
)
