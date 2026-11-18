#if canImport(UIKit)
import CoreBluetooth
import CoreImage
import CoreLocation
import CryptoKit
import LocalAuthentication
import SafariServices
import Security
import UIKit
import UniformTypeIdentifiers
import UserNotifications
import VisionKit
import WebKit
import WidgetKit

extension Scorix {
    func hostPlugins() -> [ScorixPlugin] {
        [KeystorePlugin(), BiometricPlugin(), ClipboardPlugin(), DialogPlugin(), ScannerPlugin(), BrowserPlugin(),
         SharePlugin(), LocationPlugin(), BluetoothPlugin(), NotificationPlugin(), WidgetPlugin(), CapturePlugin()]
    }

    var top: UIViewController {
        var vc: UIViewController = controller
        while let next = vc.presentedViewController {
            vc = next
        }
        return vc
    }
}

final class KeystorePlugin: KeystorePluginBase {
    func seal(_ req: KeystoreData, reply: Reply<Sealed>) {
        do {
            let key = try loadKey(req.alias) ?? createKey(req.alias)
            let box = try AES.GCM.seal(req.data, using: key)
            reply.resolve(Sealed(data: box.combined ?? Data()))
        } catch {
            reply.reject("failed", "\(error)")
        }
    }

    func open(_ req: KeystoreData, reply: Reply<Sealed>) {
        do {
            guard let key = try loadKey(req.alias) else {
                return reply.reject("not_found", "no key under \(req.alias)")
            }
            let plain = try AES.GCM.open(AES.GCM.SealedBox(combined: req.data), using: key)
            reply.resolve(Sealed(data: plain))
        } catch {
            reply.reject("failed", "\(error)")
        }
    }

    func delete(_ req: KeystoreAlias, reply: Reply<Empty>) {
        SecItemDelete(query(req.alias) as CFDictionary)
        reply.resolve(Empty())
    }

    private func query(_ alias: String) -> [String: Any] {
        [kSecClass as String: kSecClassGenericPassword, kSecAttrService as String: "scorix.\(alias)", kSecAttrAccount as String: "key"]
    }

    private func loadKey(_ alias: String) throws -> SymmetricKey? {
        var q = query(alias)
        q[kSecReturnData as String] = true
        var out: CFTypeRef?
        let status = SecItemCopyMatching(q as CFDictionary, &out)
        if status == errSecItemNotFound {
            return nil
        }
        guard status == errSecSuccess, let data = out as? Data else {
            throw NSError(domain: NSOSStatusErrorDomain, code: Int(status))
        }
        return SymmetricKey(data: data)
    }

    private func createKey(_ alias: String) throws -> SymmetricKey {
        let key = SymmetricKey(size: .bits256)
        var q = query(alias)
        q[kSecValueData as String] = key.withUnsafeBytes { Data($0) }
        q[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
        let status = SecItemAdd(q as CFDictionary, nil)
        guard status == errSecSuccess else {
            throw NSError(domain: NSOSStatusErrorDomain, code: Int(status))
        }
        return key
    }
}

final class BiometricPlugin: BiometricPluginBase {
    func status(_ req: Empty, reply: Reply<BiometricStatus>) {
        let c = LAContext()
        reply.resolve(BiometricStatus(
            biometric: c.canEvaluatePolicy(.deviceOwnerAuthenticationWithBiometrics, error: nil),
            deviceCredential: c.canEvaluatePolicy(.deviceOwnerAuthentication, error: nil)))
    }

    func authenticate(_ req: AuthenticateRequest, reply: Reply<Empty>) {
        let c = LAContext()
        if !req.cancelLabel.isEmpty {
            c.localizedCancelTitle = req.cancelLabel
        }
        let policy: LAPolicy = req.allowDeviceCredential ? .deviceOwnerAuthentication : .deviceOwnerAuthenticationWithBiometrics
        var why = req.title
        if !req.subtitle.isEmpty {
            why += why.isEmpty ? req.subtitle : "\n" + req.subtitle
        }
        reply.call.onCancel { c.invalidate() }
        c.evaluatePolicy(policy, localizedReason: why.isEmpty ? " " : why) { ok, error in
            if ok {
                return reply.resolve(Empty())
            }
            switch (error as? LAError)?.code {
            case .userCancel?, .appCancel?, .systemCancel?, .userFallback?:
                reply.reject("canceled", "authentication canceled")
            case .biometryLockout?:
                reply.reject("lockout", "too many attempts")
            case .biometryNotAvailable?, .biometryNotEnrolled?, .passcodeNotSet?:
                reply.reject("unavailable", error?.localizedDescription ?? "unavailable")
            default:
                reply.reject("failed", error?.localizedDescription ?? "failed")
            }
        }
    }
}

final class ClipboardPlugin: ClipboardPluginBase {
    func read(_ req: Empty, reply: Reply<PlainText>) {
        reply.resolve(PlainText(text: UIPasteboard.general.string ?? ""))
    }

    func write(_ req: ClipWrite, reply: Reply<Empty>) {
        var options: [UIPasteboard.OptionsKey: Any] = [:]
        if req.sensitive {
            options[.localOnly] = true
        }
        if req.clearAfterMs > 0 {
            options[.expirationDate] = Date().addingTimeInterval(Double(req.clearAfterMs) / 1000)
        }
        UIPasteboard.general.setItems([[UTType.plainText.identifier: req.text]], options: options)
        reply.resolve(Empty())
    }
}

final class DialogPlugin: NSObject, DialogPluginBase, UIDocumentPickerDelegate {
    private var pending: (([URL]?) -> Void)?
    private let places = UserDefaults.standard

    func openDocument(_ req: MimeFilter, reply: Reply<PickedDocument>) {
        let type = UTType(mimeType: req.mime) ?? .data
        present(UIDocumentPickerViewController(forOpeningContentTypes: [type], asCopy: true)) { urls in
            guard let url = urls?.first, let data = try? Data(contentsOf: url) else {
                return reply.reject("canceled", "nothing picked")
            }
            reply.resolve(PickedDocument(name: url.lastPathComponent, data: data))
        }
    }

    func createDocument(_ req: NewDocument, reply: Reply<DocumentName>) {
        guard let file = temp(req.fileName, req.data) else {
            return reply.reject("failed", "cannot stage the file")
        }
        present(UIDocumentPickerViewController(forExporting: [file], asCopy: true)) { urls in
            guard let url = urls?.first else {
                return reply.reject("canceled", "nothing picked")
            }
            reply.resolve(DocumentName(name: url.lastPathComponent))
        }
    }

    func openTree(_ req: Empty, reply: Reply<PickedPlace>) {
        present(UIDocumentPickerViewController(forOpeningContentTypes: [.folder])) { urls in
            guard let url = urls?.first else {
                return reply.reject("canceled", "nothing picked")
            }
            self.remember(url, reply: reply)
        }
    }

    func writeTree(_ req: TreeFile, reply: Reply<Empty>) {
        guard let dir = resolve(req.tree) else {
            return reply.reject("folder_gone", "the folder is no longer reachable")
        }
        let ok = dir.startAccessingSecurityScopedResource()
        defer { if ok { dir.stopAccessingSecurityScopedResource() } }
        do {
            try req.data.write(to: dir.appendingPathComponent(req.name), options: .atomic)
            reply.resolve(Empty())
        } catch {
            reply.reject("failed", error.localizedDescription)
        }
    }

    func pickFile(_ req: NewDocument, reply: Reply<PickedPlace>) {
        guard let file = temp(req.fileName, req.data) else {
            return reply.reject("failed", "cannot stage the file")
        }
        present(UIDocumentPickerViewController(forExporting: [file], asCopy: false)) { urls in
            guard let url = urls?.first else {
                return reply.reject("canceled", "nothing picked")
            }
            self.remember(url, reply: reply)
        }
    }

    func rewriteFile(_ req: FileRewrite, reply: Reply<Empty>) {
        guard let url = resolve(req.uri) else {
            return reply.reject("not_persistable", "the file is no longer reachable")
        }
        let ok = url.startAccessingSecurityScopedResource()
        defer { if ok { url.stopAccessingSecurityScopedResource() } }
        do {
            try req.data.write(to: url, options: .atomic)
            reply.resolve(Empty())
        } catch {
            reply.reject("failed", error.localizedDescription)
        }
    }

    func message(_ req: MessageBox, reply: Reply<Empty>) {
        let alert = UIAlertController(title: req.title, message: req.text, preferredStyle: .alert)
        alert.addAction(UIAlertAction(title: "OK", style: .default) { _ in reply.resolve(Empty()) })
        Scorix.shared.top.present(alert, animated: true)
    }

    private func present(_ picker: UIDocumentPickerViewController, done: @escaping ([URL]?) -> Void) {
        pending?(nil)
        pending = done
        picker.delegate = self
        Scorix.shared.top.present(picker, animated: true)
    }

    func documentPicker(_ controller: UIDocumentPickerViewController, didPickDocumentsAt urls: [URL]) {
        let done = pending
        pending = nil
        done?(urls)
    }

    func documentPickerWasCancelled(_ controller: UIDocumentPickerViewController) {
        let done = pending
        pending = nil
        done?(nil)
    }

    private func temp(_ name: String, _ data: Data) -> URL? {
        let url = FileManager.default.temporaryDirectory.appendingPathComponent(name.isEmpty ? "file" : name)
        return (try? data.write(to: url, options: .atomic)) != nil ? url : nil
    }

    private func remember(_ url: URL, reply: Reply<PickedPlace>) {
        let ok = url.startAccessingSecurityScopedResource()
        defer { if ok { url.stopAccessingSecurityScopedResource() } }
        guard let bookmark = try? url.bookmarkData() else {
            return reply.reject("not_persistable", "the place gives no lasting access")
        }
        let id = UUID().uuidString
        places.set(bookmark, forKey: "scorix.place.\(id)")
        reply.resolve(PickedPlace(id: id, name: url.lastPathComponent))
    }

    private func resolve(_ id: String) -> URL? {
        guard let bookmark = places.data(forKey: "scorix.place.\(id)") else { return nil }
        var stale = false
        return try? URL(resolvingBookmarkData: bookmark, bookmarkDataIsStale: &stale)
    }
}

final class ScannerPlugin: NSObject, ScannerPluginBase, DataScannerViewControllerDelegate, UIImagePickerControllerDelegate, UINavigationControllerDelegate {
    private var scanReply: Reply<PlainText>?
    private var imageReply: Reply<PlainText>?

    func scanQr(_ req: Empty, reply: Reply<PlainText>) {
        guard DataScannerViewController.isSupported else {
            return reply.reject("unavailable", "this device cannot scan")
        }
        guard DataScannerViewController.isAvailable else {
            return reply.reject("denied", "camera access is off for this app")
        }
        let scanner = DataScannerViewController(recognizedDataTypes: [.barcode(symbologies: [.qr])], qualityLevel: .balanced, isHighlightingEnabled: true)
        scanner.delegate = self
        scanReply = reply
        reply.call.onCancel { scanner.dismiss(animated: true) }
        Scorix.shared.top.present(scanner, animated: true) {
            do {
                try scanner.startScanning()
            } catch {
                self.finishScan(nil, error: "failed")
            }
        }
    }

    func dataScanner(_ scanner: DataScannerViewController, didAdd items: [RecognizedItem], allItems: [RecognizedItem]) {
        for item in items {
            if case .barcode(let code) = item, let text = code.payloadStringValue {
                scanner.stopScanning()
                scanner.dismiss(animated: true)
                return finishScan(text, error: nil)
            }
        }
    }

    private func finishScan(_ text: String?, error: String?) {
        guard let reply = scanReply else { return }
        scanReply = nil
        if let text = text {
            reply.resolve(PlainText(text: text))
        } else {
            reply.reject(error ?? "canceled", "no QR code read")
        }
    }

    func scanImage(_ req: Empty, reply: Reply<PlainText>) {
        let picker = UIImagePickerController()
        picker.sourceType = .photoLibrary
        picker.delegate = self
        imageReply = reply
        Scorix.shared.top.present(picker, animated: true)
    }

    func imagePickerController(_ picker: UIImagePickerController, didFinishPickingMediaWithInfo info: [UIImagePickerController.InfoKey: Any]) {
        picker.dismiss(animated: true)
        guard let reply = imageReply else { return }
        imageReply = nil
        guard let image = info[.originalImage] as? UIImage, let ci = CIImage(image: image),
              let detector = CIDetector(ofType: CIDetectorTypeQRCode, context: nil, options: [CIDetectorAccuracy: CIDetectorAccuracyHigh]),
              let code = detector.features(in: ci).compactMap({ ($0 as? CIQRCodeFeature)?.messageString }).first else {
            return reply.reject("not_found", "the image holds no QR code")
        }
        reply.resolve(PlainText(text: code))
    }

    func imagePickerControllerDidCancel(_ picker: UIImagePickerController) {
        picker.dismiss(animated: true)
        let reply = imageReply
        imageReply = nil
        reply?.reject("canceled", "no image picked")
    }
}

final class BrowserPlugin: BrowserPluginBase {
    func open(_ req: BrowserUrl, reply: Reply<Empty>) {
        guard let url = URL(string: req.url) else {
            return reply.reject("invalid_request", "not a URL: \(req.url)")
        }
        if ["http", "https"].contains(url.scheme?.lowercased() ?? "") {
            Scorix.shared.top.present(SFSafariViewController(url: url), animated: true)
            return reply.resolve(Empty())
        }
        UIApplication.shared.open(url) { ok in
            ok ? reply.resolve(Empty()) : reply.reject("unavailable", "no app can open \(req.url)")
        }
    }
}

final class SharePlugin: SharePluginBase {
    func share(_ req: ShareContent, reply: Reply<Empty>) {
        var items: [Any] = []
        if !req.text.isEmpty { items.append(req.text) }
        if let url = URL(string: req.url), !req.url.isEmpty { items.append(url) }
        if !req.data.isEmpty {
            let file = FileManager.default.temporaryDirectory.appendingPathComponent(req.fileName.isEmpty ? "shared" : req.fileName)
            if (try? req.data.write(to: file, options: .atomic)) != nil { items.append(file) }
        }
        guard !items.isEmpty else {
            return reply.reject("invalid_request", "nothing to share")
        }
        let sheet = UIActivityViewController(activityItems: items, applicationActivities: nil)
        let top = Scorix.shared.top
        sheet.popoverPresentationController?.sourceView = top.view
        sheet.popoverPresentationController?.sourceRect = CGRect(x: top.view.bounds.midX, y: top.view.bounds.maxY, width: 0, height: 0)
        sheet.completionWithItemsHandler = { _, _, _, _ in reply.resolve(Empty()) }
        top.present(sheet, animated: true)
    }
}

final class LocationPlugin: NSObject, LocationPluginBase, CLLocationManagerDelegate {
    private let manager = CLLocationManager()
    private var waiting: [(Reply<Position>, LocationRequest)] = []

    override init() {
        super.init()
        manager.delegate = self
    }

    func current(_ req: LocationRequest, reply: Reply<Position>) {
        if let last = manager.location, req.maxAgeMs > 0, -last.timestamp.timeIntervalSinceNow * 1000 <= Double(req.maxAgeMs) {
            return reply.resolve(position(last))
        }
        waiting.append((reply, req))
        manager.desiredAccuracy = req.coarse ? kCLLocationAccuracyKilometer : kCLLocationAccuracyBest
        let timeout = req.timeoutMs > 0 ? Double(req.timeoutMs) / 1000 : 30
        DispatchQueue.main.asyncAfter(deadline: .now() + timeout) { [weak self] in
            self?.fail(reply, code: "timeout", "no position within the timeout")
        }
        switch manager.authorizationStatus {
        case .notDetermined:
            manager.requestWhenInUseAuthorization()
        case .denied, .restricted:
            fail(reply, code: "denied", "location access is off for this app")
        default:
            manager.requestLocation()
        }
    }

    func locationManagerDidChangeAuthorization(_ manager: CLLocationManager) {
        switch manager.authorizationStatus {
        case .authorizedWhenInUse, .authorizedAlways:
            if !waiting.isEmpty { manager.requestLocation() }
        case .denied, .restricted:
            for (r, _) in waiting { fail(r, code: "denied", "location access is off for this app") }
        default:
            break
        }
    }

    func locationManager(_ manager: CLLocationManager, didUpdateLocations locations: [CLLocation]) {
        guard let loc = locations.last else { return }
        let all = waiting
        waiting = []
        for (r, _) in all { r.resolve(position(loc)) }
    }

    func locationManager(_ manager: CLLocationManager, didFailWithError error: Error) {
        let all = waiting
        waiting = []
        for (r, _) in all { r.reject("unavailable", error.localizedDescription) }
    }

    private func fail(_ reply: Reply<Position>, code: String, _ message: String) {
        guard let i = waiting.firstIndex(where: { $0.0 === reply }) else { return }
        waiting.remove(at: i)
        reply.reject(code, message)
    }

    private func position(_ l: CLLocation) -> Position {
        Position(latitude: l.coordinate.latitude, longitude: l.coordinate.longitude, accuracyM: l.horizontalAccuracy,
                 altitudeM: l.altitude, speedMps: max(l.speed, 0), bearingDeg: max(l.course, 0),
                 hasMotion: l.speed >= 0 && l.course >= 0, timeMs: Int(l.timestamp.timeIntervalSince1970 * 1000), provider: "corelocation")
    }
}

final class BluetoothPlugin: NSObject, BluetoothPluginBase, CBCentralManagerDelegate, CBPeripheralDelegate {
    private lazy var central = CBCentralManager(delegate: self, queue: .main)
    private var whenOn: [() -> Void] = []
    private var found: Reply<BtDevice>?
    private var peripherals: [UUID: CBPeripheral] = [:]
    private var connecting: [UUID: Reply<Empty>] = [:]
    private var reads: [String: Reply<BtValue>] = [:]
    private var writes: [String: Reply<Empty>] = [:]
    private let known = "scorix.bt.known"

    func associate(_ req: BtService, reply: Reply<BtDevice>) {
        ready(reply.call) {
            self.found = reply
            reply.call.onCancel { self.central.stopScan() }
            self.central.scanForPeripherals(withServices: [CBUUID(string: req.service)])
        }
    }

    func connect(_ req: BtAddress, reply: Reply<Empty>) {
        ready(reply.call) {
            guard let p = self.peripheral(req.address) else {
                return reply.reject("not_found", "unknown device \(req.address)")
            }
            self.connecting[p.identifier] = reply
            self.central.connect(p)
        }
    }

    func read(_ req: BtCharacteristic, reply: Reply<BtValue>) {
        withCharacteristic(req.address, req.service, req.characteristic, reply.call) { p, c in
            self.reads[c.uuid.uuidString] = reply
            p.readValue(for: c)
        }
    }

    func write(_ req: BtWrite, reply: Reply<Empty>) {
        withCharacteristic(req.address, req.service, req.characteristic, reply.call) { p, c in
            self.writes[c.uuid.uuidString] = reply
            p.writeValue(req.value, for: c, type: .withResponse)
        }
    }

    func observe(_ req: BtObserve, reply: Reply<Empty>) {
        reply.reject("unavailable", "presence of a paired device is an Android feature")
    }

    func disconnect(_ req: BtAddress, reply: Reply<Empty>) {
        if let p = peripheral(req.address) {
            central.cancelPeripheralConnection(p)
        }
        reply.resolve(Empty())
    }

    func bonded(_ req: Empty, reply: Reply<BtDevices>) {
        ready(reply.call) {
            let ids = (UserDefaults.standard.stringArray(forKey: self.known) ?? []).compactMap(UUID.init(uuidString:))
            let list = self.central.retrievePeripherals(withIdentifiers: ids)
            reply.resolve(BtDevices(devices: list.map { BtDevice(address: $0.identifier.uuidString, name: $0.name ?? "") }))
        }
    }

    func exchange(_ req: BtLine, reply: Reply<BtLine>) {
        reply.reject("unavailable", "Bluetooth Classic serial is not open to iOS apps")
    }

    func hangup(_ req: BtAddress, reply: Reply<Empty>) {
        reply.reject("unavailable", "Bluetooth Classic serial is not open to iOS apps")
    }

    private func ready(_ call: PluginCall, _ work: @escaping () -> Void) {
        switch central.state {
        case .poweredOn:
            work()
        case .unauthorized:
            call.reject("denied", "Bluetooth access is off for this app")
        case .unsupported:
            call.reject("unavailable", "this device has no Bluetooth LE")
        case .poweredOff:
            call.reject("unavailable", "Bluetooth is off")
        default:
            whenOn.append(work)
        }
    }

    private func peripheral(_ address: String) -> CBPeripheral? {
        guard let id = UUID(uuidString: address) else { return nil }
        if let p = peripherals[id] { return p }
        let p = central.retrievePeripherals(withIdentifiers: [id]).first
        if let p = p {
            p.delegate = self
            peripherals[id] = p
        }
        return p
    }

    private func withCharacteristic(_ address: String, _ service: String, _ characteristic: String, _ call: PluginCall, _ work: @escaping (CBPeripheral, CBCharacteristic) -> Void) {
        guard let p = peripheral(address), p.state == .connected else {
            return call.reject("not_connected", "connect to \(address) first")
        }
        let want = CBUUID(string: characteristic)
        if let c = p.services?.first(where: { $0.uuid == CBUUID(string: service) })?.characteristics?.first(where: { $0.uuid == want }) {
            return work(p, c)
        }
        call.reject("not_found", "no characteristic \(characteristic) in service \(service)")
    }

    func centralManagerDidUpdateState(_ central: CBCentralManager) {
        guard central.state == .poweredOn else { return }
        let all = whenOn
        whenOn = []
        all.forEach { $0() }
    }

    func centralManager(_ central: CBCentralManager, didDiscover p: CBPeripheral, advertisementData: [String: Any], rssi: NSNumber) {
        guard let reply = found else { return }
        found = nil
        central.stopScan()
        p.delegate = self
        peripherals[p.identifier] = p
        var ids = UserDefaults.standard.stringArray(forKey: known) ?? []
        if !ids.contains(p.identifier.uuidString) {
            ids.append(p.identifier.uuidString)
            UserDefaults.standard.set(ids, forKey: known)
        }
        reply.resolve(BtDevice(address: p.identifier.uuidString, name: p.name ?? ""))
    }

    func centralManager(_ central: CBCentralManager, didConnect p: CBPeripheral) {
        p.discoverServices(nil)
    }

    func centralManager(_ central: CBCentralManager, didFailToConnect p: CBPeripheral, error: Error?) {
        connecting.removeValue(forKey: p.identifier)?.reject("failed", error?.localizedDescription ?? "connect failed")
    }

    func peripheral(_ p: CBPeripheral, didDiscoverServices error: Error?) {
        for s in p.services ?? [] {
            p.discoverCharacteristics(nil, for: s)
        }
        if (p.services ?? []).isEmpty {
            connecting.removeValue(forKey: p.identifier)?.resolve(Empty())
        }
    }

    func peripheral(_ p: CBPeripheral, didDiscoverCharacteristicsFor service: CBService, error: Error?) {
        if p.services?.allSatisfy({ $0.characteristics != nil }) ?? true {
            connecting.removeValue(forKey: p.identifier)?.resolve(Empty())
        }
    }

    func peripheral(_ p: CBPeripheral, didUpdateValueFor c: CBCharacteristic, error: Error?) {
        guard let reply = reads.removeValue(forKey: c.uuid.uuidString) else { return }
        if let error = error {
            return reply.reject("failed", error.localizedDescription)
        }
        reply.resolve(BtValue(value: c.value ?? Data()))
    }

    func peripheral(_ p: CBPeripheral, didWriteValueFor c: CBCharacteristic, error: Error?) {
        guard let reply = writes.removeValue(forKey: c.uuid.uuidString) else { return }
        error == nil ? reply.resolve(Empty()) : reply.reject("failed", error!.localizedDescription)
    }
}

final class NotificationPlugin: NSObject, NotificationPluginBase, UNUserNotificationCenterDelegate {
    private let center = UNUserNotificationCenter.current()
    private let scheduledPrefix = "scorix.schedule."
    private let limit = 64

    override init() {
        super.init()
        center.delegate = self
    }

    func show(_ req: NotificationContent, reply: Reply<Empty>) {
        allowed(reply.call) {
            let id = req.key.isEmpty ? UUID().uuidString : req.key
            self.center.add(UNNotificationRequest(identifier: id, content: self.content(req), trigger: nil)) { error in
                error == nil ? reply.resolve(Empty()) : reply.reject("failed", error!.localizedDescription)
            }
        }
    }

    func schedule(_ req: NotificationSchedule, reply: Reply<Empty>) {
        allowed(reply.call) {
            self.center.getPendingNotificationRequests { pending in
                self.center.removePendingNotificationRequests(withIdentifiers: pending.map(\.identifier).filter { $0.hasPrefix(self.scheduledPrefix) })
                let now = Date().timeIntervalSince1970 * 1000
                let soonest = req.items.filter { Double($0.atMs) > now }.sorted { $0.atMs < $1.atMs }.prefix(self.limit)
                for (i, item) in soonest.enumerated() {
                    let date = Date(timeIntervalSince1970: Double(item.atMs) / 1000)
                    let parts = Calendar.current.dateComponents([.year, .month, .day, .hour, .minute, .second], from: date)
                    let trigger = UNCalendarNotificationTrigger(dateMatching: parts, repeats: false)
                    self.center.add(UNNotificationRequest(identifier: "\(self.scheduledPrefix)\(i)", content: self.content(item.content), trigger: trigger))
                }
                reply.resolve(Empty())
            }
        }
    }

    private func allowed(_ call: PluginCall, _ work: @escaping () -> Void) {
        center.requestAuthorization(options: [.alert, .sound, .badge]) { granted, _ in
            granted ? work() : call.reject("denied", "notifications are off for this app")
        }
    }

    private func content(_ c: NotificationContent) -> UNMutableNotificationContent {
        let n = UNMutableNotificationContent()
        n.title = c.title
        n.body = c.text
        n.sound = .default
        n.userInfo = ["url": c.url, "buttons": c.buttons.map(\.url)]
        if !c.buttons.isEmpty {
            let category = "scorix.buttons." + c.buttons.map(\.label).joined(separator: "|")
            let actions = c.buttons.enumerated().map { UNNotificationAction(identifier: "b\($0.offset)", title: $0.element.label, options: [.foreground]) }
            center.getNotificationCategories { existing in
                var all = existing
                all.insert(UNNotificationCategory(identifier: category, actions: actions, intentIdentifiers: []))
                self.center.setNotificationCategories(all)
            }
            n.categoryIdentifier = category
        }
        return n
    }

    func userNotificationCenter(_ center: UNUserNotificationCenter, willPresent notification: UNNotification, withCompletionHandler done: @escaping (UNNotificationPresentationOptions) -> Void) {
        done([.banner, .sound])
    }

    func userNotificationCenter(_ center: UNUserNotificationCenter, didReceive response: UNNotificationResponse, withCompletionHandler done: @escaping () -> Void) {
        let info = response.notification.request.content.userInfo
        var url = info["url"] as? String ?? ""
        if response.actionIdentifier.hasPrefix("b"), let i = Int(response.actionIdentifier.dropFirst()), let urls = info["buttons"] as? [String], i < urls.count {
            url = urls[i]
        }
        if let u = URL(string: url), !url.isEmpty {
            Scorix.shared.open(u)
        }
        done()
    }
}

final class WidgetPlugin: WidgetPluginBase {
    func setTimeline(_ req: WidgetTimeline, reply: Reply<Empty>) {
        guard let group = Bundle.main.object(forInfoDictionaryKey: "ScorixAppGroup") as? String,
              let dir = FileManager.default.containerURL(forSecurityApplicationGroupIdentifier: group) else {
            return reply.reject("unavailable", "the app declares no App Group for its widgets")
        }
        do {
            let data = try JSONSerialization.data(withJSONObject: req.toJSON())
            try data.write(to: dir.appendingPathComponent("widget-\(req.kind).json"), options: .atomic)
            WidgetCenter.shared.reloadTimelines(ofKind: req.kind)
            reply.resolve(Empty())
        } catch {
            reply.reject("failed", error.localizedDescription)
        }
    }

    func requestPin(_ req: WidgetKind, reply: Reply<Empty>) {
        reply.reject("unavailable", "iOS has no API to place a widget; the user adds it from the home screen")
    }
}

final class CapturePlugin: CapturePluginBase {
    func page(_ req: Empty, reply: Reply<PageImage>) {
        guard let wv = Scorix.shared.webView, wv.bounds.width > 0 else {
            return reply.reject("unavailable", "no page on screen")
        }
        wv.takeSnapshot(with: nil) { image, error in
            guard let png = image?.pngData() else {
                return reply.reject("unavailable", error?.localizedDescription ?? "the page is not laid out yet")
            }
            reply.resolve(PageImage(png: png))
        }
    }
}
#endif
