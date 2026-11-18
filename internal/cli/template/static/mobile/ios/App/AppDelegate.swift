// Code scaffolded by scorix. Safe to edit.

import ScorixHost
import UIKit

@main
final class AppDelegate: UIResponder, UIApplicationDelegate {
    var window: UIWindow?

    func application(_ application: UIApplication, didFinishLaunchingWithOptions launchOptions: [UIApplication.LaunchOptionsKey: Any]? = nil) -> Bool {
        Scorix.shared.start(plugins: ScorixPlugins.create())
        let window = UIWindow(frame: UIScreen.main.bounds)
        window.rootViewController = Scorix.shared.controller
        window.makeKeyAndVisible()
        self.window = window
        if let url = launchOptions?[.url] as? URL {
            Scorix.shared.open(url)
        }
        return true
    }

    func application(_ app: UIApplication, open url: URL, options: [UIApplication.OpenURLOptionsKey: Any] = [:]) -> Bool {
        Scorix.shared.open(url)
        return true
    }
}
