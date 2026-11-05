#!/usr/bin/env python3
"""
MyCaffe Seed Visual Asset Generator (WebP Studio Pipeline)
Generates high-contrast, Espresso & Crema (#1E1410, #FBF8F3, #C87D4B) bespoke studio WebP assets:
- Store Logos (1:1, 512x512 .webp)
- Onboarding & Store Hero Banners (16:9, 1280x720 .webp)
- 38 Menu Products across Store 1 (Flagship Senopati) & Store 2 (Express Sudirman):
  - 1:1 Square Menu Hero (800x800 .webp)
  - 4:3 Catalog Card (800x600 .webp)
  - 1:1 Thumbnail (400x400 .webp)
- 8 Variant Visuals (1:1, 400x400 .webp)
- 16 Addon / Sub-Addon Visuals (1:1, 400x400 .webp)
"""

import json
import math
import os
from pathlib import Path
from PIL import Image, ImageDraw, ImageFilter, ImageFont

OUT_ROOT = Path(__file__).resolve().parent.parent / "docs" / "assets" / "seeds"
CDN_BASE = "https://cdn.abbdullaziz.online/seeds"

# Brand Palette — MyCaffe Espresso & Crema
ESPRESSO_DARK = (30, 20, 16)       # #1E1410
ROAST_BROWN = (58, 38, 28)         # #3A261C
COPPER_CREMA = (200, 125, 75)      # #C87D4B
GOLD_CREMA = (229, 178, 118)       # #E5B276
IVORY_CREMA = (251, 248, 243)      # #FBF8F3
OAT_FOAM = (243, 236, 225)         # #F3ECE1
MATCHA_GREEN = (74, 124, 89)       # #4A7C59
BERRY_RUBY = (158, 43, 62)         # #9E2B3E
CITRUS_AMBER = (217, 138, 43)      # #D98A2B
SAVORY_TERRACOTTA = (184, 86, 52)  # #B85634
CHOCO_VALRHONA = (79, 45, 31)      # #4F2D1F


def load_font(size: int, bold: bool = False):
    candidates = [
        "C:/Windows/Fonts/segoeuib.ttf" if bold else "C:/Windows/Fonts/segoeui.ttf",
        "C:/Windows/Fonts/arialbd.ttf" if bold else "C:/Windows/Fonts/arial.ttf",
        "C:/Windows/Fonts/calibrib.ttf" if bold else "C:/Windows/Fonts/calibri.ttf",
    ]
    for path in candidates:
        if os.path.exists(path):
            try:
                return ImageFont.truetype(path, size)
            except Exception:
                pass
    return ImageFont.load_default()


def radial_studio_backdrop(w: int, h: int, top_rgb, bottom_rgb, glow_rgb):
    img = Image.new("RGB", (w, h), bottom_rgb)
    px = img.load()
    cx, cy = w * 0.5, h * 0.42
    max_r = math.hypot(w * 0.65, h * 0.65)
    for y in range(h):
        ty = y / max(h - 1, 1)
        base_r = top_rgb[0] * (1 - ty) + bottom_rgb[0] * ty
        base_g = top_rgb[1] * (1 - ty) + bottom_rgb[1] * ty
        base_b = top_rgb[2] * (1 - ty) + bottom_rgb[2] * ty
        for x in range(w):
            dist = math.hypot(x - cx, y - cy) / max_r
            glow = max(0.0, 1.0 - dist * dist) * 0.42
            r = int(min(255, base_r * (1 - glow) + glow_rgb[0] * glow))
            g = int(min(255, base_g * (1 - glow) + glow_rgb[1] * glow))
            b = int(min(255, base_b * (1 - glow) + glow_rgb[2] * glow))
            px[x, y] = (r, g, b)
    return img


def draw_product_illustration(draw: ImageDraw.ImageDraw, w: int, h: int, style: str, accent_rgb):
    cx, cy = w // 2, int(h * 0.43)
    scale = min(w, h) / 800.0
    s = lambda v: int(v * scale)

    # Table surface reflection ellipse
    draw.ellipse(
        [cx - s(190), cy + s(135), cx + s(190), cy + s(195)],
        fill=(20, 13, 10),
        outline=COPPER_CREMA,
        width=max(1, s(2)),
    )
    draw.ellipse(
        [cx - s(155), cy + s(145), cx + s(155), cy + s(185)],
        fill=(38, 25, 19),
    )

    if style == "iced_latte":
        # Tall crystal glass with layered espresso, milk swirl, and ice cubes
        draw.polygon(
            [
                (cx - s(95), cy - s(135)),
                (cx + s(95), cy - s(135)),
                (cx + s(75), cy + s(155)),
                (cx - s(75), cy + s(155)),
            ],
            fill=(242, 232, 218),
            outline=GOLD_CREMA,
            width=max(2, s(4)),
        )
        # Espresso top cascade layer
        draw.polygon(
            [
                (cx - s(90), cy - s(115)),
                (cx + s(90), cy - s(115)),
                (cx + s(83), cy - s(10)),
                (cx - s(83), cy - s(10)),
            ],
            fill=accent_rgb,
        )
        # Mid caramel swirl band
        draw.polygon(
            [
                (cx - s(83), cy - s(10)),
                (cx + s(83), cy - s(10)),
                (cx + s(79), cy + s(55)),
                (cx - s(79), cy + s(55)),
            ],
            fill=(198, 142, 96),
        )
        # Ice cubes
        for ox, oy in [(-42, -85), (18, -92), (-15, -35), (32, -40)]:
            draw.rounded_rectangle(
                [cx + s(ox), cy + s(oy), cx + s(ox + 42), cy + s(oy + 38)],
                radius=s(8),
                fill=(255, 252, 246),
                outline=GOLD_CREMA,
                width=max(1, s(2)),
            )
        # Cold foam dome & brass straw
        draw.chord(
            [cx - s(95), cy - s(165), cx + s(95), cy - s(105)],
            start=180,
            end=360,
            fill=IVORY_CREMA,
            outline=GOLD_CREMA,
            width=max(1, s(3)),
        )
        draw.line(
            [(cx + s(25), cy - s(205)), (cx - s(15), cy + s(120))],
            fill=COPPER_CREMA,
            width=max(3, s(10)),
        )

    elif style == "hot_cup":
        # Ceramic artisan cup & saucer with rosette latte art
        draw.ellipse(
            [cx - s(150), cy + s(95), cx + s(150), cy + s(155)],
            fill=OAT_FOAM,
            outline=COPPER_CREMA,
            width=max(2, s(4)),
        )
        # Cup handle
        draw.arc(
            [cx + s(75), cy - s(45), cx + s(155), cy + s(65)],
            start=270,
            end=90,
            fill=OAT_FOAM,
            width=max(4, s(16)),
        )
        # Cup body
        draw.chord(
            [cx - s(115), cy - s(125), cx + s(115), cy + s(125)],
            start=0,
            end=180,
            fill=ESPRESSO_DARK,
            outline=GOLD_CREMA,
            width=max(2, s(4)),
        )
        draw.rectangle([cx - s(115), cy - s(65), cx + s(115), cy + s(5)], fill=ESPRESSO_DARK)
        # Top crema rim
        draw.ellipse(
            [cx - s(115), cy - s(100), cx + s(115), cy - s(30)],
            fill=accent_rgb,
            outline=IVORY_CREMA,
            width=max(2, s(5)),
        )
        # Latte art heart/rosetta
        draw.ellipse([cx - s(45), cy - s(82), cx + s(45), cy - s(48)], fill=IVORY_CREMA)
        draw.polygon([(cx - s(35), cy - s(65)), (cx + s(35), cy - s(65)), (cx, cy - s(42))], fill=IVORY_CREMA)

    elif style == "manual_brew":
        # Glass carafe server + V60 dripper cone
        draw.polygon(
            [
                (cx - s(70), cy - s(20)),
                (cx + s(70), cy - s(20)),
                (cx + s(110), cy + s(135)),
                (cx - s(110), cy + s(135)),
            ],
            fill=accent_rgb,
            outline=GOLD_CREMA,
            width=max(2, s(4)),
        )
        # V60 cone on top
        draw.polygon(
            [
                (cx - s(95), cy - s(145)),
                (cx + s(95), cy - s(145)),
                (cx + s(35), cy - s(20)),
                (cx - s(35), cy - s(20)),
            ],
            fill=COPPER_CREMA,
            outline=IVORY_CREMA,
            width=max(2, s(3)),
        )
        draw.rectangle([cx - s(78), cy - s(25), cx + s(78), cy - s(12)], fill=GOLD_CREMA)

    elif style == "pastry":
        # Artisan ceramic plate + layered golden croissant / viennoiserie
        draw.ellipse(
            [cx - s(165), cy - s(20), cx + s(165), cy + s(155)],
            fill=IVORY_CREMA,
            outline=GOLD_CREMA,
            width=max(2, s(5)),
        )
        draw.ellipse(
            [cx - s(130), cy + s(2), cx + s(130), cy + s(132)],
            fill=OAT_FOAM,
            outline=COPPER_CREMA,
            width=max(1, s(2)),
        )
        # Flaky layered crescents
        for i, offset in enumerate([(-65, 10, 55), (-35, -10, 72), (0, -22, 85), (35, -10, 72), (65, 10, 55)]):
            ox, oy, rad = offset
            shade = (
                min(255, accent_rgb[0] + (i % 2) * 18),
                min(255, accent_rgb[1] + (i % 2) * 14),
                min(255, accent_rgb[2] + (i % 2) * 10),
            )
            draw.ellipse(
                [cx + s(ox - rad), cy + s(oy - rad // 2), cx + s(ox + rad), cy + s(oy + rad)],
                fill=shade,
                outline=ROAST_BROWN,
                width=max(1, s(3)),
            )

    elif style == "brunch":
        # Matte stoneware bowl/plate with gourmet savory garnish
        draw.ellipse(
            [cx - s(170), cy - s(65), cx + s(170), cy + s(155)],
            fill=(42, 34, 30),
            outline=GOLD_CREMA,
            width=max(2, s(5)),
        )
        draw.ellipse(
            [cx - s(142), cy - s(42), cx + s(142), cy + s(132)],
            fill=accent_rgb,
            outline=IVORY_CREMA,
            width=max(1, s(3)),
        )
        # Poached egg / protein / microgreens accents
        draw.ellipse([cx - s(65), cy - s(5), cx + s(25), cy + s(75)], fill=IVORY_CREMA)
        draw.ellipse([cx - s(38), cy + s(15), cx + s(5), cy + s(55)], fill=(235, 145, 35))
        for gx, gy in [(45, 5), (65, 35), (25, 65), (-55, 55)]:
            draw.ellipse([cx + s(gx), cy + s(gy), cx + s(gx + 28), cy + s(gy + 18)], fill=MATCHA_GREEN)

    elif style == "beans":
        # Standing matte foil roastery pouch with degassing valve & origin label
        draw.rounded_rectangle(
            [cx - s(105), cy - s(155), cx + s(105), cy + s(155)],
            radius=s(18),
            fill=ESPRESSO_DARK,
            outline=GOLD_CREMA,
            width=max(2, s(4)),
        )
        # Top seal crimps
        draw.rectangle([cx - s(105), cy - s(155), cx + s(105), cy - s(125)], fill=COPPER_CREMA)
        # Degassing valve
        draw.ellipse([cx - s(12), cy - s(105), cx + s(12), cy - s(81)], fill=GOLD_CREMA)
        # Origin card on bag
        draw.rounded_rectangle(
            [cx - s(78), cy - s(60), cx + s(78), cy + s(105)],
            radius=s(10),
            fill=IVORY_CREMA,
            outline=accent_rgb,
            width=max(2, s(4)),
        )
        draw.rectangle([cx - s(78), cy - s(60), cx + s(78), cy - s(15)], fill=accent_rgb)

    elif style == "bottle":
        # Minimalist square glass cold-brew bottle with black cap & cream label
        draw.rounded_rectangle(
            [cx - s(38), cy - s(175), cx + s(38), cy - s(130)],
            radius=s(6),
            fill=ESPRESSO_DARK,
            outline=GOLD_CREMA,
            width=max(1, s(3)),
        )
        draw.polygon(
            [
                (cx - s(32), cy - s(130)),
                (cx + s(32), cy - s(130)),
                (cx + s(82), cy - s(75)),
                (cx - s(82), cy - s(75)),
            ],
            fill=accent_rgb,
            outline=GOLD_CREMA,
            width=max(1, s(3)),
        )
        draw.rounded_rectangle(
            [cx - s(82), cy - s(80), cx + s(82), cy + s(155)],
            radius=s(16),
            fill=accent_rgb,
            outline=GOLD_CREMA,
            width=max(2, s(4)),
        )
        draw.rectangle(
            [cx - s(76), cy - s(25), cx + s(76), cy + s(95)],
            fill=IVORY_CREMA,
            outline=COPPER_CREMA,
            width=max(1, s(2)),
        )


def render_card(
    w: int,
    h: int,
    badge_text: str,
    title: str,
    subtitle: str,
    price_label: str,
    style: str,
    accent_rgb,
    store_tag: str = "MYCAFFE ROASTERY",
) -> Image.Image:
    img = radial_studio_backdrop(w, h, ROAST_BROWN, ESPRESSO_DARK, accent_rgb)
    draw = ImageDraw.Draw(img)
    scale = min(w, h) / 800.0
    s = lambda v: max(1, int(v * scale))

    # Subtle outer frame
    draw.rounded_rectangle(
        [s(22), s(22), w - s(22), h - s(22)],
        radius=s(24),
        outline=(95, 68, 48),
        width=s(2),
    )

    # Top pill badge
    f_badge = load_font(s(20), bold=True)
    draw.rounded_rectangle(
        [s(44), s(42), s(310), s(84)],
        radius=s(20),
        fill=ESPRESSO_DARK,
        outline=COPPER_CREMA,
        width=s(2),
    )
    draw.text((s(62), s(50)), badge_text.upper()[:24], font=f_badge, fill=GOLD_CREMA)

    # Top-right store tag
    if price_label:
        draw.rounded_rectangle(
            [w - s(220), s(42), w - s(44), s(84)],
            radius=s(20),
            fill=COPPER_CREMA,
        )
        draw.text((w - s(198), s(50)), price_label, font=f_badge, fill=IVORY_CREMA)

    # Central illustration
    draw_product_illustration(draw, w, h, style, accent_rgb)

    # Bottom typography plate
    plate_top = int(h * 0.73)
    draw.rounded_rectangle(
        [s(40), plate_top, w - s(40), h - s(38)],
        radius=s(20),
        fill=(24, 16, 13),
        outline=COPPER_CREMA,
        width=s(2),
    )
    f_title = load_font(s(32), bold=True)
    f_sub = load_font(s(20), bold=False)
    f_tag = load_font(s(16), bold=True)

    draw.text((s(64), plate_top + s(18)), title[:34], font=f_title, fill=IVORY_CREMA)
    draw.text((s(64), plate_top + s(62)), subtitle[:52], font=f_sub, fill=GOLD_CREMA)
    draw.text((s(64), plate_top + s(96)), store_tag, font=f_tag, fill=(168, 142, 122))
    return img


def save_webp(img: Image.Image, rel_path: str) -> dict:
    full_path = OUT_ROOT / rel_path
    full_path.parent.mkdir(parents=True, exist_ok=True)
    img.save(full_path, format="WEBP", quality=88, method=6)
    size_bytes = full_path.stat().st_size
    return {
        "rel_path": rel_path,
        "cdn_url": f"{CDN_BASE}/{rel_path}",
        "width": img.width,
        "height": img.height,
        "size_bytes": size_bytes,
    }


PRODUCTS = [
    # Store 1 (Flagship Senopati) - Original 5 Core Products (d0000001..d0000005)
    ("d0000001-0000-4000-8000-000000000001", "classic-iced-latte", "Signature Series", "Classic Iced Latte", "Double Ristretto • Fresh Milk • Artisan Aren", "Rp 24.000", "iced_latte", COPPER_CREMA, "MYCAFFE SENOPATI ROASTERY"),
    ("d0000002-0000-4000-8000-000000000002", "caffe-latte", "Espresso & Coffee", "Caffe Latte", "Velvet Steamed Milk • Silky Microfoam", "Rp 26.000", "hot_cup", COPPER_CREMA, "MYCAFFE SENOPATI ROASTERY"),
    ("d0000003-0000-4000-8000-000000000003", "americano-double-shot", "Espresso & Coffee", "Americano (Double Shot)", "Double Ristretto • Filtered Spring Water", "Rp 20.000", "hot_cup", ROAST_BROWN, "MYCAFFE SENOPATI ROASTERY"),
    ("d0000004-0000-4000-8000-000000000004", "v60-single-origin-gayo", "Manual Brew", "V60 Single Origin Gayo", "Floral Citrus • Highland Aceh Gayo #1", "Rp 32.000", "manual_brew", CITRUS_AMBER, "MYCAFFE SENOPATI ROASTERY"),
    ("d0000005-0000-4000-8000-000000000005", "butter-croissant-artisanal", "Artisan Bakery", "Butter Croissant Artisanal", "French AOC Cultured Butter • 27 Laminated Layers", "Rp 24.000", "pastry", GOLD_CREMA, "MYCAFFE SENOPATI ROASTERY"),
    # Store 2 (Express Counter) - Core Product (d0000006)
    ("d0000006-0000-4000-8000-000000000006", "express-cold-brew", "Quick Grab Coffee", "Express Cold Brew", "18-Hour Slow Steeped • Bold Chocolate Finish", "Rp 22.000", "iced_latte", ROAST_BROWN, "MYCAFFE EXPRESS SUDIRMAN"),
    # Store 1 Expansion (d0000007..d0000029)
    ("d0000007-0000-4000-8000-000000000007", "kopi-susu-gula-aren-senopati", "Signature Series", "Es Kopi Susu Aren Senopati", "Organic West Java Palm Nectar • Double Espresso", "Rp 26.000", "iced_latte", COPPER_CREMA, "MYCAFFE SENOPATI ROASTERY"),
    ("d0000008-0000-4000-8000-000000000008", "salted-caramel-cloud-macchiato", "Signature Series", "Salted Caramel Cloud Macchiato", "Sea Salt Cold Foam • Madagascar Vanilla • Espresso", "Rp 34.000", "iced_latte", CITRUS_AMBER, "MYCAFFE SENOPATI ROASTERY"),
    ("d0000009-0000-4000-8000-000000000009", "pistachio-crema-cold-latte", "Signature Series", "Pistachio Crema Cold Latte", "Roasted Sicilian Pistachio Cream • Double Ristretto", "Rp 38.000", "iced_latte", MATCHA_GREEN, "MYCAFFE SENOPATI ROASTERY"),
    ("d0000010-0000-4000-8000-000000000010", "yuzu-espresso-tonica", "Signature Series", "Yuzu Espresso Tonica", "Japanese Yuzu Citrus • Botanical Tonic • Espresso", "Rp 34.000", "iced_latte", CITRUS_AMBER, "MYCAFFE SENOPATI ROASTERY"),
    ("d0000011-0000-4000-8000-000000000011", "velvet-cappuccino", "Espresso & Coffee", "Velvet Cappuccino", "Rich Crema • Dusted Valrhona Cocoa • Microfoam", "Rp 28.000", "hot_cup", COPPER_CREMA, "MYCAFFE SENOPATI ROASTERY"),
    ("d0000012-0000-4000-8000-000000000012", "flat-white-double-ristretto", "Espresso & Coffee", "Flat White Double Ristretto", "20g Double Ristretto • Thin Silky Textured Milk", "Rp 30.000", "hot_cup", ROAST_BROWN, "MYCAFFE SENOPATI ROASTERY"),
    ("d0000013-0000-4000-8000-000000000013", "piccolo-latte-4oz", "Espresso & Coffee", "Piccolo Latte (4 oz)", "Single Ristretto Shot in Warm Demitasse Glass", "Rp 24.000", "hot_cup", COPPER_CREMA, "MYCAFFE SENOPATI ROASTERY"),
    ("d0000014-0000-4000-8000-000000000014", "japanese-iced-filter-kintamani", "Manual Brew", "Japanese Iced Bali Kintamani", "Flash-Chilled Pour Over • Sweet Orange & Brown Sugar", "Rp 34.000", "manual_brew", CITRUS_AMBER, "MYCAFFE SENOPATI ROASTERY"),
    ("d0000015-0000-4000-8000-000000000015", "kalita-wave-flores-bajawa", "Manual Brew", "Kalita Wave Flores Bajawa", "Full-Bodied Nutty Cocoa & Caramelized Spices", "Rp 35.000", "manual_brew", COPPER_CREMA, "MYCAFFE SENOPATI ROASTERY"),
    ("d0000016-0000-4000-8000-000000000016", "nitro-cold-brew-cascade", "Manual Brew", "Nitro Cold Brew Cascade", "Nitrogen-Infused Single Origin • Creamy Stout Head", "Rp 36.000", "iced_latte", ROAST_BROWN, "MYCAFFE SENOPATI ROASTERY"),
    ("d0000017-0000-4000-8000-000000000017", "pain-au-chocolat-valrhona", "Artisan Bakery", "Pain au Chocolat Valrhona", "Double Batons of 66% Dark Valrhona Chocolate", "Rp 28.000", "pastry", CHOCO_VALRHONA, "MYCAFFE SENOPATI ROASTERY"),
    ("d0000018-0000-4000-8000-000000000018", "almond-kouign-amann", "Artisan Bakery", "Caramelized Almond Kouign-Amann", "Brittany Salted Butter Crust • Toasted Frangipane", "Rp 32.000", "pastry", GOLD_CREMA, "MYCAFFE SENOPATI ROASTERY"),
    ("d0000019-0000-4000-8000-000000000019", "basque-burnt-cheesecake", "Artisan Bakery", "Basque Burnt Cheesecake Slice", "Creamy San Sebastian Center • Caramelized Top", "Rp 42.000", "pastry", CITRUS_AMBER, "MYCAFFE SENOPATI ROASTERY"),
    ("d0000020-0000-4000-8000-000000000020", "uji-ceremonial-matcha-latte", "Matcha & Non-Coffee", "Uji Ceremonial Matcha Latte", "First-Harvest Kyoto Uji Matcha • Whisked Chasen", "Rp 36.000", "iced_latte", MATCHA_GREEN, "MYCAFFE SENOPATI ROASTERY"),
    ("d0000021-0000-4000-8000-000000000021", "hojicha-roasted-oat-latte", "Matcha & Non-Coffee", "Hojicha Roasted Oat Latte", "Charcoal-Roasted Green Tea • Smoky Caramel Notes", "Rp 35.000", "hot_cup", COPPER_CREMA, "MYCAFFE SENOPATI ROASTERY"),
    ("d0000022-0000-4000-8000-000000000022", "single-origin-dark-choco-karo", "Matcha & Non-Coffee", "72% Single Origin Choco Karo", "Sumatran Craft Cacao Ganache • Steamed Fresh Milk", "Rp 34.000", "hot_cup", CHOCO_VALRHONA, "MYCAFFE SENOPATI ROASTERY"),
    ("d0000023-0000-4000-8000-000000000023", "hibiscus-berry-artisan-tea", "Matcha & Non-Coffee", "Hibiscus Berry Cold Brew Tea", "Roselle Petals • Wild Forest Berries • Mint", "Rp 28.000", "iced_latte", BERRY_RUBY, "MYCAFFE SENOPATI ROASTERY"),
    ("d0000024-0000-4000-8000-000000000024", "truffle-scrambled-egg-brioche", "All-Day Brunch", "Truffle Egg & Smoked Beef Brioche", "Free-Range Soft Scramble • Black Truffle Aioli", "Rp 58.000", "brunch", SAVORY_TERRACOTTA, "MYCAFFE SENOPATI KITCHEN"),
    ("d0000025-0000-4000-8000-000000000025", "crispy-chicken-sambal-matah-bowl", "All-Day Brunch", "Crispy Chicken Sambal Matah Bowl", "Balinese Lemongrass Torch Ginger Relish • Onsen Egg", "Rp 52.000", "brunch", SAVORY_TERRACOTTA, "MYCAFFE SENOPATI KITCHEN"),
    ("d0000026-0000-4000-8000-000000000026", "wagyu-beef-lasagna-al-forno", "All-Day Brunch", "Wagyu Beef Lasagna Al Forno", "Slow-Braised Wagyu Ragù • Aged Parmigiano Béchamel", "Rp 68.000", "brunch", BERRY_RUBY, "MYCAFFE SENOPATI KITCHEN"),
    ("d0000027-0000-4000-8000-000000000027", "truffle-parmesan-pommes-frites", "All-Day Brunch", "Truffle Parmesan Pommes Frites", "Hand-Cut Crispy Shoestring • Grana Padano • Rosemary", "Rp 42.000", "brunch", GOLD_CREMA, "MYCAFFE SENOPATI KITCHEN"),
    ("d0000028-0000-4000-8000-000000000028", "roastery-house-blend-beans", "Whole Beans", "Espresso Crema House Blend (Beans)", "70% Aceh Gayo Arabica • 30% Temanggung Fine Robusta", "Rp 95.000", "beans", COPPER_CREMA, "MYCAFFE ROASTERY ATELIER"),
    ("d0000029-0000-4000-8000-000000000029", "aceh-gayo-wine-process-beans", "Whole Beans", "Aceh Gayo Natural Wine Process", "1500 MASL Takengon • Fermented Berry & Dark Cocoa", "Rp 125.000", "beans", BERRY_RUBY, "MYCAFFE ROASTERY ATELIER"),
    # Store 2 (Express Sudirman MRT) Expansion (d0000030..d0000038)
    ("d0000030-0000-4000-8000-000000000030", "es-kopi-susu-komuter", "Quick Grab Coffee", "Es Kopi Susu Komuter MRT", "Creamy Aren Espresso • Fast-Serve Sealed Cup", "Rp 20.000", "iced_latte", COPPER_CREMA, "MYCAFFE EXPRESS SUDIRMAN"),
    ("d0000031-0000-4000-8000-000000000031", "iced-shaken-brown-sugar-americano", "Quick Grab Coffee", "Shaken Brown Sugar Americano", "Aerated Double Espresso • Organic Molasses Foam", "Rp 22.000", "iced_latte", ROAST_BROWN, "MYCAFFE EXPRESS SUDIRMAN"),
    ("d0000032-0000-4000-8000-000000000032", "express-iced-matcha-boost", "Quick Grab Non-Coffee", "Express Iced Matcha Boost", "Kyoto Matcha Blend • Cold Fresh Milk", "Rp 26.000", "iced_latte", MATCHA_GREEN, "MYCAFFE EXPRESS SUDIRMAN"),
    ("d0000033-0000-4000-8000-000000000033", "iced-belgian-choco-malt", "Quick Grab Non-Coffee", "Iced Belgian Choco Malt", "Dark Cocoa • Crunchy Malt Foam", "Rp 25.000", "iced_latte", CHOCO_VALRHONA, "MYCAFFE EXPRESS SUDIRMAN"),
    ("d0000034-0000-4000-8000-000000000034", "botol-kopi-susu-aren-500ml", "Bottled Grab & Go", "Botol Kopi Susu Aren (500ml)", "Chilled 3-Serving Bottle • Sealed Fresh Daily", "Rp 55.000", "bottle", COPPER_CREMA, "MYCAFFE EXPRESS SUDIRMAN"),
    ("d0000035-0000-4000-8000-000000000035", "botol-cold-brew-black-500ml", "Bottled Grab & Go", "Botol Cold Brew Black (500ml)", "Zero Sugar Single Origin Cold Brew Concentrate", "Rp 52.000", "bottle", ROAST_BROWN, "MYCAFFE EXPRESS SUDIRMAN"),
    ("d0000036-0000-4000-8000-000000000036", "smoked-beef-cheddar-croissant", "Grab & Go Bakery", "Smoked Beef & Cheddar Croissant", "Pressed Warm Butter Croissant • Melted Red Cheddar", "Rp 34.000", "pastry", SAVORY_TERRACOTTA, "MYCAFFE EXPRESS SUDIRMAN"),
    ("d0000037-0000-4000-8000-000000000037", "cinnamon-sugar-cruffin", "Grab & Go Bakery", "Cinnamon Sugar Brioche Cruffin", "Flaky Croissant Muffin • Vanilla Bean Custard", "Rp 26.000", "pastry", GOLD_CREMA, "MYCAFFE EXPRESS SUDIRMAN"),
    ("d0000038-0000-4000-8000-000000000038", "banana-walnut-coffee-cake", "Grab & Go Bakery", "Banana Walnut Espresso Loaf", "Caramelized Raja Banana • Toasted California Walnuts", "Rp 24.000", "pastry", COPPER_CREMA, "MYCAFFE EXPRESS SUDIRMAN"),
]

VARIANTS = [
    ("size-reguler", "Cup Size", "Reguler (12 oz)", "Standard Balanced Ratio", "hot_cup", COPPER_CREMA),
    ("size-large", "Cup Size", "Large (16 oz)", "Extra Volume + Double Shot", "iced_latte", GOLD_CREMA),
    ("serve-hot", "Temperature", "Disajikan Panas (Hot)", "Steamed 62°C Microfoam", "hot_cup", SAVORY_TERRACOTTA),
    ("serve-iced", "Temperature", "Disajikan Dingin (Iced)", "Crystal Cube Ice", "iced_latte", CITRUS_AMBER),
    ("beans-250g", "Roastery Pack", "Kemasan 250g Pouch", "Valve-Sealed Whole Beans", "beans", COPPER_CREMA),
    ("beans-500g", "Roastery Pack", "Kemasan 500g Value", "Roastery Bulk Pack", "beans", BERRY_RUBY),
    ("bottle-500ml", "Bottle Size", "Botol 500 ml", "3–4 Gelas Saji", "bottle", COPPER_CREMA),
    ("bottle-1000ml", "Bottle Size", "Botol 1 Liter", "7–8 Gelas Saji", "bottle", GOLD_CREMA),
]

ADDONS = [
    ("milk-dairy", "Milk Selection", "Fresh Dairy Milk", "+Rp 0 (Default)", "hot_cup", COPPER_CREMA),
    ("milk-oat", "Milk Selection", "Oat Milk Barista Blend", "+Rp 8.000", "iced_latte", GOLD_CREMA),
    ("milk-almond", "Milk Selection", "Roasted Almond Milk", "+Rp 8.000", "iced_latte", CITRUS_AMBER),
    ("milk-soy", "Milk Selection", "Organic Soy Milk", "+Rp 6.000", "hot_cup", MATCHA_GREEN),
    ("sugar-normal", "Sweetness", "Normal Sugar (100%)", "+Rp 0", "iced_latte", COPPER_CREMA),
    ("sugar-less", "Sweetness", "Less Sugar (50%)", "+Rp 0", "iced_latte", GOLD_CREMA),
    ("sugar-zero", "Sweetness", "No Sugar (0%)", "+Rp 0", "hot_cup", ROAST_BROWN),
    ("ice-normal", "Ice Level", "Normal Crystal Ice", "+Rp 0", "iced_latte", CITRUS_AMBER),
    ("ice-less", "Ice Level", "Less Ice (70%)", "+Rp 0", "iced_latte", GOLD_CREMA),
    ("shot-extra-espresso", "Espresso Upgrade", "Extra Single Ristretto", "+Rp 6.000", "hot_cup", ROAST_BROWN),
    ("shot-decaf", "Espresso Upgrade", "Swiss Water Decaf Bean", "+Rp 5.000", "beans", MATCHA_GREEN),
    ("sub-standard-pour", "Plant Milk Sub-Addon", "Standard Barista Pour", "+Rp 0", "hot_cup", COPPER_CREMA),
    ("sub-oat-cold-foam", "Plant Milk Sub-Addon", "Whipped Oat Cold Foam", "+Rp 4.000", "iced_latte", GOLD_CREMA),
    ("sub-caramel-drizzle", "Plant Milk Sub-Addon", "Salted Caramel Lattice", "+Rp 5.000", "iced_latte", CITRUS_AMBER),
    ("pastry-warmed", "Bakery Serve", "Hangatkan di Oven (Warmed)", "+Rp 0", "pastry", GOLD_CREMA),
    ("pastry-butter-jam", "Bakery Serve", "Ekstra French Butter & Berry Jam", "+Rp 6.000", "pastry", BERRY_RUBY),
]

ONBOARDING_BANNERS = [
    ("hero-qr-table-ordering", "QR TABLE ORDERING", "Scan Meja • Pesan Tanpa Antre • Bayar QRIS Instan", "16:9 Onboarding Banner", "iced_latte", COPPER_CREMA),
    ("hero-pos-cashier-kds", "OMNICHANNEL POS & KDS", "Sinkronisasi Kasir, Barista, Dapur & Layar Antrean Real-Time", "16:9 Onboarding Banner", "hot_cup", GOLD_CREMA),
    ("hero-flagship-roastery", "MYCAFFE FLAGSHIP SENOPATI", "Artisan Roastery • Manual Brew Bar • All-Day Dining", "Jl. Senopati No. 88, Jakarta Selatan", "manual_brew", CITRUS_AMBER),
    ("hero-express-grab-go", "MYCAFFE EXPRESS SUDIRMAN MRT", "60-Second Commuter Coffee • Cold Brew & Fresh Croissant", "Stasiun MRT Istora Mandiri Pintu B", "bottle", MATCHA_GREEN),
]

STORE_LOGOS = [
    ("flagship-senopati-logo", "FLAGSHIP ROASTERY", "MYCAFFE SENOPATI", "Est. Jakarta • Specialty Coffee & Kitchen", "hot_cup", COPPER_CREMA),
    ("express-sudirman-logo", "EXPRESS GRAB & GO", "MYCAFFE SUDIRMAN", "Commuter Coffee Bar • MRT Istora", "bottle", GOLD_CREMA),
]


def main():
    manifest = {
        "cdn_base": CDN_BASE,
        "products": {},
        "variants": {},
        "addons": {},
        "onboarding": {},
        "stores": {},
    }

    # 1. Generate Store Logos (1:1, 512x512)
    for slug, badge, title, sub, style, accent in STORE_LOGOS:
        img = render_card(512, 512, badge, title, sub, "", style, accent, "OFFICIAL TENANT BRAND")
        manifest["stores"][slug] = save_webp(img, f"stores/{slug}.webp")

    # 2. Generate Onboarding Banners (16:9, 1280x720)
    for slug, badge, title, sub, style, accent in ONBOARDING_BANNERS:
        img = render_card(1280, 720, badge, title, sub, "MYCAFFE POS", style, accent, "ESPRESSO & CREMA DESIGN SYSTEM")
        manifest["onboarding"][slug] = save_webp(img, f"onboarding/{slug}.webp")

    # 3. Generate Variant Visuals (1:1, 400x400)
    for slug, badge, title, sub, style, accent in VARIANTS:
        img = render_card(400, 400, badge, title, sub, "", style, accent, "VARIANT SPECIFICATION")
        manifest["variants"][slug] = save_webp(img, f"variants/{slug}.webp")

    # 4. Generate Addon / Sub-Addon Visuals (1:1, 400x400)
    for slug, badge, title, sub, style, accent in ADDONS:
        img = render_card(400, 400, badge, title, sub, "", style, accent, "MODIFIER & SUB-ADDON")
        manifest["addons"][slug] = save_webp(img, f"addons/{slug}.webp")

    # 5. Generate 38 Product Images in 3 Aspect Ratios / Sizes:
    #    - 1:1 Square (800x800) -> products/<slug>-1x1.webp
    #    - 4:3 Card (800x600)   -> products/<slug>-4x3.webp
    #    - 1:1 Thumb (400x400)  -> products/<slug>-thumb.webp
    for prod_id, slug, cat, title, sub, price, style, accent, store_tag in PRODUCTS:
        img_1x1 = render_card(800, 800, cat, title, sub, price, style, accent, store_tag)
        img_4x3 = render_card(800, 600, cat, title, sub, price, style, accent, store_tag)
        img_thumb = img_1x1.resize((400, 400), Image.Resampling.LANCZOS)

        meta_1x1 = save_webp(img_1x1, f"products/{slug}-1x1.webp")
        meta_4x3 = save_webp(img_4x3, f"products/{slug}-4x3.webp")
        meta_thumb = save_webp(img_thumb, f"products/{slug}-thumb.webp")

        manifest["products"][prod_id] = {
            "slug": slug,
            "name": title,
            "primary_url": meta_1x1["cdn_url"],
            "size_bytes": meta_1x1["size_bytes"],
            "object_key": f"seeds/products/{slug}-1x1.webp",
            "variants_json": {
                "original": meta_1x1["cdn_url"],
                "square_1x1": meta_1x1["cdn_url"],
                "card_4x3": meta_4x3["cdn_url"],
                "thumb": meta_thumb["cdn_url"],
            },
        }

    manifest_path = OUT_ROOT / "manifest.json"
    manifest_path.write_text(json.dumps(manifest, indent=2), encoding="utf-8")
    total_files = (
        len(manifest["stores"])
        + len(manifest["onboarding"])
        + len(manifest["variants"])
        + len(manifest["addons"])
        + len(manifest["products"]) * 3
    )
    print(f"Generated {total_files} WebP files in {OUT_ROOT} and wrote {manifest_path}")


if __name__ == "__main__":
    main()

