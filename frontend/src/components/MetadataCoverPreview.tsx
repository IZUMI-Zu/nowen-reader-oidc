interface MetadataCoverPreviewProps {
  coverUrl: string;
  title?: string;
}

// Metadata result covers are loaded from third-party hosts. Suppress the
// Referer so a preview cannot disclose the self-hosted Nowen Reader origin.
export function MetadataCoverPreview({ coverUrl, title }: MetadataCoverPreviewProps) {
  return (
    <img
      src={coverUrl}
      alt={title || "cover"}
      referrerPolicy="no-referrer"
      className="w-12 h-16 object-cover rounded flex-shrink-0 bg-card-hover"
      onError={(event) => {
        event.currentTarget.style.display = "none";
      }}
    />
  );
}
