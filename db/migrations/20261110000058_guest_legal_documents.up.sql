-- 20261110000058_guest_legal_documents.up.sql
-- Guest-facing legal texts for QR ordering, kept apart from the tenant SaaS documents that onboarding consent reads.
-- content_sha256 is computed from the text itself so a guest can verify what they read.

ALTER TABLE legal_documents
    DROP CONSTRAINT chk_legal_documents_type,
    ADD CONSTRAINT chk_legal_documents_type CHECK (doc_type IN (
        'terms_of_service', 'privacy_policy_pdp', 'dpa_tenant', 'refund_policy',
        'guest_privacy_notice', 'guest_order_terms'
    ));

INSERT INTO legal_documents (doc_type, version, locale, title, content_markdown, content_sha256)
SELECT doc_type, version, locale, title, body, encode(sha256(convert_to(body, 'UTF8')), 'hex')
FROM (VALUES
    (
        'guest_privacy_notice', '1.0.0-draft', 'id-ID',
        'Pemberitahuan Privasi Tamu',
        'Draf, menunggu tinjauan penasihat hukum.

Pemberitahuan ini berlaku saat Anda memesan dari meja gerai lewat kode QR yang memakai Cassy.

1. Siapa yang bertanggung jawab
Gerai tempat Anda memesan adalah pengendali data pribadi Anda. AzureDevID, penyedia perangkat lunak Cassy, memproses data atas nama gerai sesuai Undang-Undang Nomor 27 Tahun 2022 tentang Pelindungan Data Pribadi.

2. Data yang dikumpulkan
Nama panggilan yang Anda isi saat memesan. Bila dikosongkan, pesanan memakai nama "Tamu".
Alamat email, hanya bila Anda meminta struk dikirim lewat email.
Isi pesanan, nomor meja, dan status pembayaran.
Satu cookie sesi meja yang berlaku 3 jam, agar pesanan tetap terhubung ke perangkat Anda.

3. Tujuan
Nama dipakai staf untuk memanggil pesanan Anda. Email dipakai satu kali untuk mengirim struk. Cookie dipakai agar hanya perangkat Anda yang bisa melihat pesanan Anda.

4. Pembayaran
Pembayaran QRIS diproses oleh Midtrans atas nama gerai. Cassy tidak menerima maupun menyimpan data rekening, kartu, atau dompet digital Anda.

5. Masa simpan
Alamat email lengkap dihapus paling lambat 90 hari setelah struk dikirim. Setelah itu yang tersisa hanya bentuk tersamar untuk riwayat pengiriman.
Nama dan isi pesanan disimpan bersama catatan transaksi gerai selama gerai wajib menyimpan catatan tersebut.

6. Tanpa pelacakan iklan
Halaman pesan tidak memuat skrip iklan atau analitik pihak ketiga.

7. Hak Anda
Anda dapat meminta akses, perbaikan, atau penghapusan data kepada gerai. Penghapusan dapat dibatasi oleh kewajiban gerai menyimpan catatan transaksi. Pertanyaan tentang pemrosesan oleh Cassy dapat dikirim ke legal@abbdullaziz.online.'
    ),
    (
        'guest_privacy_notice', '1.0.0-draft', 'en-US',
        'Guest Privacy Notice',
        'Draft, pending review by legal counsel.

This notice applies when you order from a table through a QR code that runs on Cassy.

1. Who is responsible
The outlet where you order is the controller of your personal data. AzureDevID, the provider of the Cassy software, processes data on behalf of the outlet under Indonesian Law Number 27 of 2022 on Personal Data Protection.

2. Data collected
The name you enter when ordering. If left empty, the order uses the name "Guest".
Your email address, only when you ask for the receipt by email.
The order contents, table number, and payment status.
One table session cookie valid for 3 hours, so the order stays linked to your device.

3. Purpose
Staff use the name to call your order. The email is used once to send the receipt. The cookie lets only your device see your order.

4. Payment
QRIS payments are processed by Midtrans on behalf of the outlet. Cassy neither receives nor stores your bank, card, or e-wallet details.

5. Retention
The full email address is deleted no later than 90 days after the receipt is sent. After that only a masked form remains in the delivery history.
The name and order contents are kept with the outlet transaction records for as long as the outlet must keep those records.

6. No ad tracking
The ordering page loads no third-party advertising or analytics scripts.

7. Your rights
You may ask the outlet for access to, correction of, or deletion of your data. Deletion may be limited by the outlet duty to keep transaction records. Questions about processing by Cassy can be sent to legal@abbdullaziz.online.'
    ),
    (
        'guest_order_terms', '1.0.0-draft', 'id-ID',
        'Ketentuan Pemesanan Tamu',
        'Draf, menunggu tinjauan penasihat hukum.

Ketentuan ini berlaku saat Anda memesan dari meja gerai lewat kode QR yang memakai Cassy.

1. Penjual
Gerai adalah penjual makanan dan minuman yang Anda pesan dan bertanggung jawab atas mutu, harga, dan penyajiannya. AzureDevID hanya menyediakan perangkat lunak Cassy.

2. Harga
Harga, biaya layanan, dan pajak ditetapkan gerai dan ditampilkan sebelum Anda membayar. Bila harga berubah sebelum pesanan dibuat, Anda diminta memeriksa ulang totalnya.

3. Pembayaran
Pesanan dibayar di muka lewat QRIS yang diproses Midtrans atas nama gerai. Pesanan dikirim ke dapur setelah pembayaran diterima. Kode QR memiliki batas waktu; bila habis, Anda dapat meminta kode baru tanpa membuat pesanan baru.

4. Pembatalan dan pengembalian dana
Pembatalan pesanan dan pengembalian dana ditangani langsung oleh gerai. Bila pesanan dibatalkan setelah Anda membayar, atau Anda tidak sengaja membayar dua kali, hubungi staf gerai untuk pengembalian dana.

5. Batas tanggung jawab
AzureDevID tidak bertanggung jawab atas mutu produk, ketersediaan menu, atau layanan gerai. Gangguan sistem dilaporkan ke staf gerai agar pesanan Anda dapat diproses secara manual.

6. Hukum yang berlaku
Ketentuan ini tunduk pada hukum Republik Indonesia.'
    ),
    (
        'guest_order_terms', '1.0.0-draft', 'en-US',
        'Guest Ordering Terms',
        'Draft, pending review by legal counsel.

These terms apply when you order from a table through a QR code that runs on Cassy.

1. Seller
The outlet is the seller of the food and drinks you order and is responsible for their quality, price, and service. AzureDevID only provides the Cassy software.

2. Prices
Prices, service charge, and tax are set by the outlet and shown before you pay. If a price changes before the order is placed, you are asked to check the total again.

3. Payment
Orders are paid in advance by QRIS, processed by Midtrans on behalf of the outlet. The order goes to the kitchen once payment is received. A QR code has a time limit; when it runs out you can ask for a new code without placing a new order.

4. Cancellations and refunds
Cancellations and refunds are handled by the outlet. If the order is cancelled after you paid, or you paid twice by mistake, ask the outlet staff for a refund.

5. Limitation of liability
AzureDevID is not responsible for product quality, menu availability, or outlet service. Report a system problem to the outlet staff so your order can be handled manually.

6. Governing law
These terms are governed by the laws of the Republic of Indonesia.'
    )
) AS d(doc_type, version, locale, title, body)
ON CONFLICT (doc_type, version, locale) DO NOTHING;
