-- 20261111000060_guest_legal_v1_1.up.sql
-- Guest texts v1.1 (PRD v3 N6): QRIS, GoPay and ShopeePay, pickup or table service, and what the page keeps
-- on the guest device. Version 1.0 stays in the table for the record but is no longer shown.

INSERT INTO legal_documents (doc_type, version, locale, title, content_markdown, content_sha256)
SELECT doc_type, version, locale, title, body, encode(sha256(convert_to(body, 'UTF8')), 'hex')
FROM (VALUES
    (
        'guest_privacy_notice', '1.1.0-draft', 'id-ID',
        'Pemberitahuan Privasi Tamu',
        'Draf, menunggu tinjauan penasihat hukum.

Pemberitahuan ini berlaku saat Anda memesan dari meja gerai lewat kode QR yang memakai Cassy.

1. Siapa yang bertanggung jawab
Gerai tempat Anda memesan adalah pengendali data pribadi Anda. AzureDevID, penyedia perangkat lunak Cassy, memproses data atas nama gerai sesuai Undang-Undang Nomor 27 Tahun 2022 tentang Pelindungan Data Pribadi.

2. Data yang kami terima
Nama yang Anda isi saat memesan. Bila dikosongkan, pesanan memakai nama "Tamu".
Alamat email, hanya bila Anda meminta struk dikirim lewat email.
Isi pesanan, nomor meja, metode bayar yang Anda pilih, dan status pembayaran.

3. Yang disimpan di perangkat Anda
Satu cookie sesi meja, berlaku 3 jam, agar hanya perangkat Anda yang bisa melihat pesanan Anda.
Isi keranjang, disimpan di tab browser dan hilang saat tab ditutup.
Penanda pesanan yang sedang berjalan, paling lama 3 jam, agar Anda bisa kembali ke halaman pembayaran atau status bila tab tertutup.
Pilihan bunyi pemberitahuan, sampai Anda mengubahnya.
Semua ini hanya dibaca oleh halaman pesan gerai dan tidak dikirim ke pihak lain.

4. Tujuan
Nama dipakai staf untuk memanggil atau mengantar pesanan Anda. Email dipakai satu kali untuk mengirim struk.

5. Pembayaran
Pembayaran lewat QRIS, GoPay, atau ShopeePay diproses oleh Midtrans atas nama gerai. Saat Anda memilih GoPay atau ShopeePay, halaman membuka aplikasi tersebut dan pembayaran berlangsung di sana. Cassy tidak menerima maupun menyimpan data rekening, kartu, PIN, atau saldo dompet digital Anda.

6. Masa simpan
Alamat email lengkap dihapus paling lambat 90 hari setelah struk dikirim. Setelah itu yang tersisa hanya bentuk tersamar untuk riwayat pengiriman.
Nama dan isi pesanan disimpan bersama catatan transaksi gerai selama gerai wajib menyimpan catatan tersebut.

7. Tanpa pelacakan iklan
Halaman pesan tidak memuat skrip iklan atau analitik pihak ketiga.

8. Hak Anda
Anda dapat meminta akses, perbaikan, atau penghapusan data kepada gerai. Penghapusan dapat dibatasi oleh kewajiban gerai menyimpan catatan transaksi. Pertanyaan tentang pemrosesan oleh Cassy dapat dikirim ke legal@abbdullaziz.online.'
    ),
    (
        'guest_privacy_notice', '1.1.0-draft', 'en-US',
        'Guest Privacy Notice',
        'Draft, pending review by legal counsel.

This notice applies when you order from a table through a QR code that runs on Cassy.

1. Who is responsible
The outlet where you order is the controller of your personal data. AzureDevID, the provider of the Cassy software, processes data on behalf of the outlet under Indonesian Law Number 27 of 2022 on Personal Data Protection.

2. Data we receive
The name you enter when ordering. If left empty, the order uses the name "Guest".
Your email address, only when you ask for the receipt by email.
The order contents, table number, the payment method you chose, and the payment status.

3. What stays on your device
One table session cookie, valid for 3 hours, so only your device can see your order.
Your cart, kept in the browser tab and gone when you close the tab.
A marker for the order in progress, for up to 3 hours, so you can return to the payment or status page if the tab closes.
Your notification sound choice, until you change it.
Only the outlet ordering page reads these, and they are not sent to anyone else.

4. Purpose
Staff use the name to call or bring your order. The email is used once to send the receipt.

5. Payment
Payments by QRIS, GoPay, or ShopeePay are processed by Midtrans on behalf of the outlet. When you choose GoPay or ShopeePay, the page opens that app and you pay there. Cassy neither receives nor stores your bank, card, PIN, or e-wallet balance details.

6. Retention
The full email address is deleted no later than 90 days after the receipt is sent. After that only a masked form remains in the delivery history.
The name and order contents are kept with the outlet transaction records for as long as the outlet must keep those records.

7. No ad tracking
The ordering page loads no third-party advertising or analytics scripts.

8. Your rights
You may ask the outlet for access to, correction of, or deletion of your data. Deletion may be limited by the outlet duty to keep transaction records. Questions about processing by Cassy can be sent to legal@abbdullaziz.online.'
    ),
    (
        'guest_order_terms', '1.1.0-draft', 'id-ID',
        'Ketentuan Pemesanan Tamu',
        'Draf, menunggu tinjauan penasihat hukum.

Ketentuan ini berlaku saat Anda memesan dari meja gerai lewat kode QR yang memakai Cassy.

1. Penjual
Gerai adalah penjual makanan dan minuman yang Anda pesan dan bertanggung jawab atas mutu, harga, dan penyajiannya. AzureDevID hanya menyediakan perangkat lunak Cassy.

2. Harga
Harga, biaya layanan, dan pajak ditetapkan gerai dan ditampilkan sebelum Anda membayar. Bila harga berubah sebelum pesanan dibuat, Anda diminta memeriksa ulang totalnya.

3. Pembayaran
Pesanan dibayar di muka lewat QRIS, GoPay, atau ShopeePay, sesuai metode yang diaktifkan gerai. Pembayaran diproses Midtrans atas nama gerai. Pesanan dikirim ke dapur setelah pembayaran diterima. Setiap tagihan memiliki batas waktu; bila habis, Anda dapat membuat tagihan baru atau memilih metode lain tanpa membuat pesanan baru.

4. Pengambilan atau pengantaran
Gerai menentukan cara pesanan sampai ke Anda. Di gerai ambil sendiri, halaman memberi tahu saat pesanan siap diambil di bar. Di gerai antar ke meja, staf mengantar pesanan dan halaman memberi tahu saat pesanan sudah diantar.

5. Pembatalan dan pengembalian dana
Pembatalan pesanan dan pengembalian dana ditangani langsung oleh gerai. Bila pesanan dibatalkan setelah Anda membayar, atau Anda tidak sengaja membayar dua kali, tunjukkan nomor pesanan kepada staf gerai untuk pengembalian dana.

6. Batas tanggung jawab
AzureDevID tidak bertanggung jawab atas mutu produk, ketersediaan menu, atau layanan gerai. Bila halaman sedang dalam pemeliharaan atau mengalami gangguan, pesanan yang sudah dibayar tetap tercatat; laporkan kepada staf gerai agar pesanan Anda dapat diproses secara manual.

7. Hukum yang berlaku
Ketentuan ini tunduk pada hukum Republik Indonesia.'
    ),
    (
        'guest_order_terms', '1.1.0-draft', 'en-US',
        'Guest Ordering Terms',
        'Draft, pending review by legal counsel.

These terms apply when you order from a table through a QR code that runs on Cassy.

1. Seller
The outlet is the seller of the food and drinks you order and is responsible for their quality, price, and service. AzureDevID only provides the Cassy software.

2. Prices
Prices, service charge, and tax are set by the outlet and shown before you pay. If a price changes before the order is placed, you are asked to check the total again.

3. Payment
Orders are paid in advance by QRIS, GoPay, or ShopeePay, depending on the methods the outlet has turned on. Payments are processed by Midtrans on behalf of the outlet. The order goes to the kitchen once payment is received. Each payment request has a time limit; when it runs out you can create a new one or choose another method without placing a new order.

4. Pickup or table service
The outlet decides how the order reaches you. At a pickup outlet, the page tells you when the order is ready at the bar. At a table service outlet, staff bring the order and the page tells you when it has been delivered.

5. Cancellations and refunds
Cancellations and refunds are handled by the outlet. If the order is cancelled after you paid, or you paid twice by mistake, show the order number to the outlet staff for a refund.

6. Limitation of liability
AzureDevID is not responsible for product quality, menu availability, or outlet service. If the page is under maintenance or has a problem, orders you already paid for stay recorded; tell the outlet staff so your order can be handled manually.

7. Governing law
These terms are governed by the laws of the Republic of Indonesia.'
    )
) AS d(doc_type, version, locale, title, body)
ON CONFLICT (doc_type, version, locale) DO NOTHING;

UPDATE legal_documents
SET is_active = false
WHERE doc_type IN ('guest_privacy_notice', 'guest_order_terms') AND version = '1.0.0-draft';
