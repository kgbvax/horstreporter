Horstreporter est un outil pour les radioamateurs. Son but est de rendre facilement explorables les conditions de propagation **actuelles** sur les bandes HF.  
C'est en pratique une interface alternative pour <a href="https://www.pskreporter.info" target="_blank">pskreporter</a>.  


## Pour commencer
1. Saisissez votre **Locator** (p. ex. JO32, FN31AB) dans le champ en haut. Vous pouvez aussi cliquer sur l'icône de marqueur pour détecter automatiquement votre position.
2. Cliquez sur **Go**. HorstReporter se connecte au flux en direct et récupère les rapports FT8/FT4 récents où vous-même ou des stations de votre grille apparaissez comme émetteur ou récepteur.
3. Regardez la carte se remplir ! Les données se mettent à jour en temps réel tant que le flux est actif.

## Fonctions & commandes
* **Filtres :** Activez ou désactivez certaines bandes, ou appliquez un seuil de SNR minimal pour n'afficher que les spots assez forts pour des contacts CW ou SSB.
* **Projection :** Basculez entre *Mercator* (la carte du monde plate habituelle) et *Azimutale* (une vue en grand cercle centrée sur votre position — pratique pour lire les azimuts d'antenne et les distances).
* **Styles :** Choisissez comment visualiser les données :
    * *Grid :* Regroupe les spots en carrés Maidenhead. La couleur indique la bande dominante, l'opacité la force du signal.
    * *Active Area :* Trace des polygones dynamiques englobant les spots regroupés pour montrer l'empreinte globale de propagation.

* **Réglages :** Sous la liste des bandes, **Layers** active les calques de la carte et les sources de spots (RBN, WSPR, DX Cluster, étiquettes DXCC, couleurs des pays, prévision), **Display** règle l'âge des spots, les seuils SNR, la distance de regroupement, le zoom automatique et plus encore, et **Notifications** gère les alertes push.
* Si vous ne voyez pas assez de données pour votre carré, activez **Adj. Squares** qui récupère aussi les données des 8 carrés voisins.

## Interaction avec la carte
* Survolez les éléments colorés de la carte pour voir des statistiques détaillées (SNR min/max/moyen) et les meilleurs rapports de la zone.
* Cliquez n'importe où sur la carte pour définir rapidement un nouveau locator cible et actualiser les données.  



# Origine
Réalisé par DL9ET (<a href="https://mastodon.radio/@dl9et" target="_blank">Mastodon anglais</a>, <a href="https://radiosocial.de/@dl9et" target="_blank">Mastodon allemand</a>, <a href="https://www.qrz.com/db/DL9ET" target="_blank"> sur QRZ</a>) pour ~~gratter une démangeaison~~ repérer des occasions de DX en SSB.


L'idée vient de DK3JF que j'observais explorer les ondes avec WSJT-X à Morokulien. J'ai d'abord bricolé quelque chose à partir du flux de notre SDR, ce qui était très utile, mais j'ai ensuite réalisé qu'on n'a pas besoin d'un SDR : le WSJT-X de l'OM d'à côté suffit largement. D'où l'approche actuelle : utiliser les données pskreporter de votre grille.

Si cette approche vous intéresse, jetez un œil à <a href="https://hf.dxview.org" target="_blank">https://hf.dxview.org</a>.

Comme tout en radioamateur, ceci est à considérer comme expérimental et peut imploser à tout moment. 95 % de ce projet a été créé avec Google Gemini. Contactez-moi pour tout retour ou demande.  
Plus d'informations sont peut-être disponibles sur mon blog : <a href="https://dl9et.darc.de/tags/horstreporter/" target="_blank">https://dl9et.darc.de/tags/horstreporter/</a>

Le dragon en peluche vert en bas à gauche s'appelle « Horst-Kevin ».

## Note d'implémentation
Au cœur du système, horstreporter s'abonne *une seule fois* au firehose de pskreporter et assure lui-même le fan-out / filtrage 1:n pour chaque client. C'est un choix délibéré pour ne pas surcharger les serveurs pskreporter. Horstreporter conserve aussi un court historique afin que les nouveaux clients n'aient pas à attendre l'arrivée des données via le flux d'événements. L'essentiel de la logique de visualisation et d'interaction se fait dans le navigateur, tandis que le scoring dérivé des conditions DX est calculé côté serveur et exposé via `/api/dx_conditions`.

Ce projet est open source sous licence Affero GPL.

# MENTIONS LÉGALES + CONFIDENTIALITÉ

Ingomar Otter DL9ET, Vortlager Damm 6, Lengerich, Allemagne

Le serveur horstreporter.kgbvax.net ne traite aucune donnée personnelle au sens du RGPD. Les préférences de l'utilisateur sont mémorisées dans son navigateur via le « local storage ».  
