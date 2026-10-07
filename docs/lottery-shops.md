# Permanent lottery shops

The [example configuration](../config.example.yaml) enables 56 lottery shops and
one select-ticket exchange shop whose definitions have no global closing time in
the supported local master snapshot. These are `MasterLotteryShop.id` values,
not `MasterLottery.id` values. The server sends this list in `lotteryShop`;
loading master data alone does not enable a shop.

The inventory joins `MasterLotteryShop.releaseLabel` to `MasterRelease` and
requires released entries with an empty `closeAt`. All associated `MasterLottery`
or `MasterSelectTicketLottery` releases also have no global closing time, and no
weekly release periods apply. The supplied definitions include releases through
version 6.8.10. Compare IDs and release conditions with your own master snapshot
when changing client data; this list describes the supported snapshot rather than
the live official catalog.

Permanent availability of an entry does not waive player eligibility, draw
limits, payment or ticket expiry. The tutorial is one draw; the starter free
lottery has beginner conditions and a 14-draw limit. Most ticket shops are hidden
without their matching ticket. Anniversary and event-earned tickets can have a
permanent redemption shop even when their acquisition campaign has ended.
The starter select-ticket pack is advertised with a seven-day purchase window
and an expiring ticket. Enabling its exchange entry does not implement real-money
purchase settlement.

## General, tutorial and story shops (7)

| Shop ID | Master label | Master name |
| --- | --- | --- |
| 65000013 | `lotteryShop_13` | スカウトチケットガチャ |
| 65000016 | `lotteryShop_16` | チュートリアルガチャ |
| 65000151 | `lotteryShop_151_Free` | スタートダッシュ毎日無料ガチャ |
| 65000494 | `lotteryShop_494_initial_SS` | 初期実装SSスタイル確定チケットガチャ |
| 65000671 | `lotteryShop_671_story` | メインストーリースペシャル10連チケットガチャ |
| 65000672 | `lotteryShop_672_story_SS` | メインストーリースペシャルSS確定チケットガチャ |
| 65001005 | `lotteryShop_1005_regular` | レギュラーガチャ |

## Recommended SS ticket shops (8)

| Shop ID | Master label | Master name |
| --- | --- | --- |
| 65000711 | `lotteryShop_711_ticket` | SS確定レコメンドチケットガチャ 斬属性 ver. |
| 65000712 | `lotteryShop_712_ticket` | SS確定レコメンドチケットガチャ 突属性 ver. |
| 65000713 | `lotteryShop_713_ticket` | SS確定レコメンドチケットガチャ 打属性 ver. |
| 65000714 | `lotteryShop_714_ticket` | SS確定レコメンドチケットガチャ HEALER ver. |
| 65000715 | `lotteryShop_715_ticket` | SS確定レコメンドチケットガチャ BUFFER ver. |
| 65001531 | `lotteryShop_1531_ticket` | SS確定レコメンドチケットガチャ 火属性 ver. |
| 65001532 | `lotteryShop_1532_ticket` | SS確定レコメンドチケットガチャ 氷属性 ver. |
| 65001535 | `lotteryShop_1535_ticket` | SS確定レコメンドチケットガチャ 闇属性 ver. |

## SS ticket shops with gauge labels (38)

These shops share the displayed name `SS確定チケットガチャ`. Their distinct IDs,
master labels and matching tickets select different reward pools. The character
text in a label does not by itself guarantee that character as the reward.

| Shop ID | Master label |
| --- | --- |
| 65000664 | `lotteryShop_664_YShirakawa_gauge` |
| 65000683 | `lotteryShop_683_TTojo04_gauge` |
| 65000724 | `lotteryShop_724_MYanagi_gauge` |
| 65000754 | `lotteryShop_754_AKanzaki_gauge` |
| 65000843 | `lotteryShop_843_YIzumi_gauge` |
| 65000865 | `lotteryShop_865_YBungo_gauge` |
| 65000888 | `lotteryShop_888_TTojo_gauge` |
| 65000899 | `lotteryShop_899_MKiryu_gauge` |
| 65000923 | `lotteryShop_923_SSakuraba_gauge` |
| 65000934 | `lotteryShop_934_YShirakawa_gauge` |
| 65000946 | `lotteryShop_946_MSatsuki_gauge` |
| 65000947 | `lotteryShop_947_MAikawa_gauge` |
| 65000967 | `lotteryShop_967_MKurosawa_gauge` |
| 65000975 | `lotteryShop_975_HOgasawara_gauge` |
| 65001006 | `lotteryShop_1006_YIzumi_gauge` |
| 65001015 | `lotteryShop_1015_SMinase_gauge` |
| 65001024 | `lotteryShop_1024_EAoi_gauge` |
| 65001043 | `lotteryShop_1043_FMikoto_gauge` |
| 65001054 | `lotteryShop_1054_RKayamori_gauge` |
| 65001096 | `lotteryShop_1096_YShirakawa_gauge` |
| 65001149 | `lotteryShop_1149_MdAngelis_gauge` |
| 65001156 | `lotteryShop_1156_YBungo_gauge` |
| 65001165 | `lotteryShop_1165_MTenne_gauge` |
| 65001186 | `lotteryShop_1186_MYanagi_gauge` |
| 65001205 | `lotteryShop_1205_KHiiragi_gauge` |
| 65001227 | `lotteryShop_1227_EAoi_gauge` |
| 65001235 | `lotteryShop_1235_MKiryu_gauge` |
| 65001243 | `lotteryShop_1243_KAsakura_gauge` |
| 65001285 | `lotteryShop_1285_CSkopovskaya_gauge` |
| 65001296 | `lotteryShop_1296_RKayamori_gauge` |
| 65001297 | `lotteryShop_1297_IMinase_gauge` |
| 65001303 | `lotteryShop_1303_TKunimi_gauge` |
| 65001325 | `lotteryShop_1325_BIYamawakii_gauge` |
| 65001335 | `lotteryShop_1335_KMaruyama_gauge` |
| 65001360 | `lotteryShop_1360_YShirakawa_gauge` |
| 65001469 | `lotteryShop_1469_1500_gauge` |
| 65001473 | `lotteryShop_1473_EmaC_gauge` |
| 65001483 | `lotteryShop_1483_EmaD_gauge` |

## Other permanent ticket shops (3)

| Shop ID | Master label | Master name |
| --- | --- | --- |
| 65001502 | `lotteryShop_1502_ticket_SS` | おすすめセレクション SS確定ガチャ |
| 65001526 | `lotteryShop_1526_attribute` | 属性ADMIRAL1体確定！4.5th Anniversaryスペシャル10連チケットガチャ |
| 65001527 | `lotteryShop_1527_bingo` | SS確定チケットガチャ |

## Permanent select-ticket exchange entry (1)

| Shop ID | Master label | Master name |
| --- | --- | --- |
| 65800011 | `lotteryShop_StartDashSelectTicket01_11` | 7日間限定！スタートダッシュ セレクトチケットパック |
