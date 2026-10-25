package dev.scorix

import android.Manifest
import android.annotation.SuppressLint
import android.bluetooth.BluetoothDevice
import android.bluetooth.BluetoothGatt
import android.bluetooth.BluetoothGattCallback
import android.bluetooth.BluetoothGattCharacteristic
import android.bluetooth.BluetoothManager
import android.bluetooth.BluetoothProfile
import android.bluetooth.le.ScanFilter
import android.bluetooth.le.ScanResult
import android.companion.AssociationInfo
import android.companion.AssociationRequest
import android.companion.BluetoothLeDeviceFilter
import android.companion.CompanionDeviceManager
import android.content.Context
import android.content.Intent
import android.content.IntentSender
import android.content.pm.PackageManager
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.os.ParcelUuid
import java.util.UUID

/**
 * LE GATT through CompanionDeviceManager's chooser (no scan or location permission) and RFCOMM to
 * bonded devices. The app declares BLUETOOTH_CONNECT itself, as with CAMERA.
 */
@SuppressLint("MissingPermission") // checked by needConnect before any GATT call
internal class BluetoothPlugin : BluetoothPluginBase() {
    private val main = Handler(Looper.getMainLooper())
    private val links = HashMap<String, Link>()
    private val sockets = HashMap<String, Classic>()
    // RFCOMM sockets block, so exchanges run off the main thread, one at a time per socket.
    private val io = java.util.concurrent.Executors.newCachedThreadPool()

    override fun connect(req: BtAddress, reply: Reply<Empty>) = needConnect(reply.call) { connect(req.address, reply.call) }

    override fun read(req: BtCharacteristic, reply: Reply<BtValue>) =
        needConnect(reply.call) { op(req.address, req.service, req.characteristic, reply.call) { g, c -> readOf(g, c) } }

    override fun write(req: BtWrite, reply: Reply<Empty>) =
        needConnect(reply.call) { op(req.address, req.service, req.characteristic, reply.call) { g, c -> writeOf(g, c, req.value) } }

    override fun bonded(req: Empty, reply: Reply<BtDevices>) = needConnect(reply.call) { bonded(reply) }

    override fun exchange(req: BtLine, reply: Reply<BtLine>) = needConnect(reply.call) { exchangeLine(req, reply) }

    override fun hangup(req: BtAddress, reply: Reply<Empty>) {
        synchronized(sockets) { sockets.remove(req.address) }?.close()
        reply.resolve(Empty())
    }

    override fun disconnect(req: BtAddress, reply: Reply<Empty>) {
        links.remove(req.address)?.gatt?.close()
        reply.resolve(Empty())
    }

    override fun associate(req: BtService, reply: Reply<BtDevice>) {
        val call = reply.call
        val a = call.activity ?: return call.reject("no_activity", "pairing needs an Activity on screen")
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.O) {
            return call.reject("unavailable", "companion device pairing needs Android 8")
        }
        val filter = BluetoothLeDeviceFilter.Builder()
            .setScanFilter(ScanFilter.Builder().setServiceUuid(ParcelUuid.fromString(req.service)).build())
            .build()
        val request = AssociationRequest.Builder().addDeviceFilter(filter).setSingleDevice(false).build()
        val cdm = a.getSystemService(CompanionDeviceManager::class.java)
        val sent = { sender: IntentSender ->
            call.startSenderForResult(sender) { code, data ->
                if (code != android.app.Activity.RESULT_OK || data == null) {
                    call.reject("canceled", "no device was chosen")
                } else {
                    deviceOf(data)?.let {
                        reply.resolve(BtDevice(address = it.address, name = it.name ?: ""))
                    } ?: call.reject("failed", "the chooser returned no device")
                }
            }
        }
        val cb = object : CompanionDeviceManager.Callback() {
            @Deprecated("Below API 33 the only callback")
            override fun onDeviceFound(sender: IntentSender) = sent(sender)

            override fun onAssociationPending(sender: IntentSender) = sent(sender)

            override fun onFailure(error: CharSequence?) = call.reject("failed", error?.toString() ?: "pairing failed")
        }
        @Suppress("DEPRECATION")
        cdm.associate(request, cb, main)
    }

    // The app's CompanionDeviceService hears when the device comes in range: for a computer that
    // advertises only while it asks, that is the ask. The app declares the service and permission.
    override fun observe(req: BtObserve, reply: Reply<Empty>) {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.S) {
            return reply.reject("unavailable", "presence needs Android 12")
        }
        val c = ScorixHost.appContext ?: return reply.reject("unavailable", "no application context")
        val cdm = c.getSystemService(CompanionDeviceManager::class.java)
        try {
            @Suppress("DEPRECATION")
            if (req.on) cdm.startObservingDevicePresence(req.address) else cdm.stopObservingDevicePresence(req.address)
        } catch (e: Exception) {
            return reply.reject("failed", e.message ?: "presence refused")
        }
        reply.resolve(Empty())
    }

    private fun adapterOf(call: PluginCall): android.bluetooth.BluetoothAdapter? {
        val c = ScorixHost.appContext ?: return null.also { call.reject("unavailable", "no application context") }
        val a = c.getSystemService(BluetoothManager::class.java)?.adapter
            ?: return null.also { call.reject("unavailable", "this phone has no Bluetooth") }
        if (!a.isEnabled) return null.also { call.reject("off", "Bluetooth is off") }
        return a
    }

    private fun bonded(reply: Reply<BtDevices>) {
        val a = adapterOf(reply.call) ?: return
        reply.resolve(BtDevices(devices = a.bondedDevices.map { BtDevice(address = it.address, name = it.name ?: "") }))
    }

    /** A bonded device's RFCOMM service, kept open between exchanges. */
    private class Classic(val socket: android.bluetooth.BluetoothSocket) {
        val reader = socket.inputStream.bufferedReader()
        val writer = socket.outputStream

        fun close() = try {
            socket.close()
        } catch (_: java.io.IOException) {
        }
    }

    private fun exchangeLine(req: BtLine, reply: Reply<BtLine>) {
        val call = reply.call
        val a = adapterOf(call) ?: return
        val address = req.address
        val uuid = UUID.fromString(req.uuid)
        val line = req.line
        val cancelled = java.util.concurrent.atomic.AtomicBoolean(false)
        call.onCancel {
            cancelled.set(true)
            synchronized(sockets) { sockets.remove(address) }?.close()
        }
        io.execute {
            // Only a socket kept from before may have died quietly with the other side; it gets one
            // fresh try. A new socket failing, or Go giving up, ends the call.
            for (attempt in 0..1) {
                if (cancelled.get()) return@execute
                val kept = synchronized(sockets) { sockets[address] }
                val s = kept ?: try {
                    val sock = a.getRemoteDevice(address).createRfcommSocketToServiceRecord(uuid)
                    a.cancelDiscovery()
                    sock.connect()
                    Classic(sock).also { synchronized(sockets) { sockets[address] = it } }
                } catch (e: java.io.IOException) {
                    main.post { call.reject("failed", e.message ?: "could not connect") }
                    return@execute
                }
                try {
                    synchronized(s) {
                        s.writer.write(line)
                        s.writer.write('\n'.code)
                        s.writer.flush()
                        val answer = s.reader.readLine() ?: throw java.io.IOException("closed")
                        main.post { reply.resolve(BtLine(line = answer.toByteArray(Charsets.UTF_8))) }
                    }
                    return@execute
                } catch (e: java.io.IOException) {
                    synchronized(sockets) { sockets.remove(address) }
                    s.close()
                    if (kept == null || attempt == 1) {
                        main.post { call.reject("failed", e.message ?: "connection lost") }
                        return@execute
                    }
                }
            }
        }
    }

    // An LE filter hands back a ScanResult; EXTRA_DEVICE still carries it on API 33+.
    private fun deviceOf(data: Intent): BluetoothDevice? {
        @Suppress("DEPRECATION")
        return when (val x = data.getParcelableExtra<android.os.Parcelable>(CompanionDeviceManager.EXTRA_DEVICE)) {
            is ScanResult -> x.device
            is BluetoothDevice -> x
            else -> if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.UPSIDE_DOWN_CAKE) {
                data.getParcelableExtra(CompanionDeviceManager.EXTRA_ASSOCIATION, AssociationInfo::class.java)
                    ?.associatedDevice?.bleDevice?.device
            } else {
                null
            }
        }
    }

    private fun needConnect(call: PluginCall, then: () -> Unit) {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.S) return then()
        val c = ScorixHost.appContext ?: return call.reject("unavailable", "no application context")
        if (c.checkSelfPermission(Manifest.permission.BLUETOOTH_CONNECT) == PackageManager.PERMISSION_GRANTED) return then()
        call.requestPermissions(arrayOf(Manifest.permission.BLUETOOTH_CONNECT)) { ok ->
            if (ok) then() else call.reject("denied", "the user did not allow nearby devices")
        }
    }

    /** One connection, one operation in flight: GATT itself refuses a second until the first ends. */
    private inner class Link(val address: String) : BluetoothGattCallback() {
        var gatt: BluetoothGatt? = null
        var ready: PluginCall? = null
        var pending: PluginCall? = null

        override fun onConnectionStateChange(g: BluetoothGatt, status: Int, state: Int) {
            main.post {
                if (state == BluetoothProfile.STATE_CONNECTED && status == BluetoothGatt.GATT_SUCCESS) {
                    // 517 so a whole message fits one write: BlueZ delivers prepared writes in pieces.
                    if (!g.requestMtu(517)) g.discoverServices()
                    return@post
                }
                links.remove(address)
                g.close()
                ready?.reject("failed", "connection lost (status $status)")
                pending?.reject("failed", "connection lost (status $status)")
                ready = null
                pending = null
            }
        }

        override fun onMtuChanged(g: BluetoothGatt, mtu: Int, status: Int) {
            main.post { g.discoverServices() }
        }

        override fun onServicesDiscovered(g: BluetoothGatt, status: Int) {
            main.post {
                if (status == BluetoothGatt.GATT_SUCCESS) ready?.resolve() else ready?.reject("failed", "service discovery failed ($status)")
                ready = null
            }
        }

        @Deprecated("Below API 33 the only read callback")
        override fun onCharacteristicRead(g: BluetoothGatt, c: BluetoothGattCharacteristic, status: Int) {
            @Suppress("DEPRECATION")
            done(status, c.value)
        }

        override fun onCharacteristicRead(g: BluetoothGatt, c: BluetoothGattCharacteristic, value: ByteArray, status: Int) = done(status, value)

        override fun onCharacteristicWrite(g: BluetoothGatt, c: BluetoothGattCharacteristic, status: Int) = done(status, null)

        private fun done(status: Int, value: ByteArray?) {
            main.post {
                val p = pending ?: return@post
                pending = null
                if (status != BluetoothGatt.GATT_SUCCESS) return@post p.reject("failed", "GATT status $status")
                p.resolve(BtValue(value = value ?: ByteArray(0)).toJson())
            }
        }
    }

    private fun connect(address: String, call: PluginCall) {
        val c = ScorixHost.appContext ?: return call.reject("unavailable", "no application context")
        val adapter = c.getSystemService(BluetoothManager::class.java)?.adapter
            ?: return call.reject("unavailable", "this phone has no Bluetooth")
        if (!adapter.isEnabled) return call.reject("off", "Bluetooth is off")
        links.remove(address)?.gatt?.close()
        val link = Link(address)
        link.ready = call
        links[address] = link
        link.gatt = adapter.getRemoteDevice(address).connectGatt(c, false, link, BluetoothDevice.TRANSPORT_LE)
        call.onCancel {
            links.remove(address)?.gatt?.close()
        }
    }

    private fun op(address: String, service: String, characteristic: String, call: PluginCall, run: (BluetoothGatt, BluetoothGattCharacteristic) -> Boolean) {
        val link = links[address] ?: return call.reject("not_connected", "connect first")
        val g = link.gatt ?: return call.reject("not_connected", "connect first")
        if (link.pending != null) return call.reject("busy", "another operation is in flight")
        val ch = g.getService(UUID.fromString(service))
            ?.getCharacteristic(UUID.fromString(characteristic))
            ?: return call.reject("not_found", "no such characteristic")
        link.pending = call
        if (!run(g, ch)) {
            link.pending = null
            call.reject("failed", "the operation could not start")
        }
    }

    private fun readOf(g: BluetoothGatt, c: BluetoothGattCharacteristic) = g.readCharacteristic(c)

    private fun writeOf(g: BluetoothGatt, c: BluetoothGattCharacteristic, value: ByteArray): Boolean =
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            g.writeCharacteristic(c, value, BluetoothGattCharacteristic.WRITE_TYPE_DEFAULT) == BluetoothGatt.GATT_SUCCESS
        } else {
            @Suppress("DEPRECATION")
            c.writeType = BluetoothGattCharacteristic.WRITE_TYPE_DEFAULT
            @Suppress("DEPRECATION")
            c.value = value
            @Suppress("DEPRECATION")
            g.writeCharacteristic(c)
        }
}
